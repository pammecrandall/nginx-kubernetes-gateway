package status

import (
	"context"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/gateway-api/apis/v1beta1"

	"github.com/nginxinc/nginx-kubernetes-gateway/internal/state"
)

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 . Updater

// Updater updates statuses of the Gateway API resources.
type Updater interface {
	// Update updates the statuses of the resources.
	Update(context.Context, state.Statuses)
}

// UpdaterConfig holds configuration parameters for Updater.
type UpdaterConfig struct {
	// GatewayCtlrName is the name of the Gateway controller.
	GatewayCtlrName string
	// GatewayClassName is the name of the GatewayClass resource.
	GatewayClassName string
	// Client is a Kubernetes API client.
	Client client.Client
	// Logger holds a logger to be used.
	Logger logr.Logger
	// Clock is used as a source of time for the LastTransitionTime field in Conditions in resource statuses.
	Clock Clock
}

// updaterImpl updates statuses of the Gateway API resources.
//
// It has the following limitations:
//
// (1) It doesn't understand the leader election. Only the leader must report the statuses of the resources. Otherwise,
// multiple replicas will step on each other when trying to report statuses for the same resources.
// FIXME(pleshakov): address limitation (1)
//
// (2) It is not smart. It will update the status of a resource (make an API call) even if it hasn't changed.
// FIXME(pleshakov) address limitation (2)
//
// (3) It is synchronous, which means the status reporter can slow down the event loop.
// Consider the following cases:
// (a) Sometimes the Gateway will need to update statuses of all resources it handles, which could be ~1000. Making 1000
// status API calls sequentially will take time.
// (b) k8s API can become slow or even timeout. This will increase every update status API call.
// Making updaterImpl asynchronous will prevent it from adding variable delays to the event loop.
// FIXME(pleshakov) address limitation (3)
//
// (4) It doesn't retry on failures. This means there is a chance that some resources will not have up-to-do statuses.
// Statuses are important part of the Gateway API, so we need to ensure that the Gateway always keep the resources
// statuses up-to-date.
// FIXME(pleshakov): address limitation (4)
//
// (5) It doesn't clear the statuses of a resources that are no longer handled by the Gateway. For example, if
// an HTTPRoute resource no longer has the parentRef to the Gateway resources, the Gateway must update the status
// of the resource to remove the status about the removed parentRef.
// FIXME(pleshakov): address limitation (5)
//
// (6) If another controllers changes the status of the Gateway/HTTPRoute resource so that the information set by our
// Gateway is removed, our Gateway will not restore the status until the EventLoop invokes the StatusUpdater as a
// result of processing some other new change to a resource(s).
// FIXME(pleshakov): Figure out if this is something that needs to be addressed.

// (7) To support new resources, updaterImpl needs to be modified. Consider making updaterImpl extendable, so that it
// goes along the Open-closed principle.
// FIXME(pleshakov): address limitation (7)
type updaterImpl struct {
	cfg UpdaterConfig
}

// NewUpdater creates a new Updater.
func NewUpdater(cfg UpdaterConfig) Updater {
	return &updaterImpl{
		cfg: cfg,
	}
}

func (upd *updaterImpl) Update(ctx context.Context, statuses state.Statuses) {
	// FIXME(pleshakov) Merge the new Conditions in the status with the existing Conditions
	// FIXME(pleshakov) Skip the status update (API call) if the status hasn't changed.

	if statuses.GatewayClassStatus != nil {
		upd.update(ctx, types.NamespacedName{Name: upd.cfg.GatewayClassName}, &v1beta1.GatewayClass{}, func(object client.Object) {
			gc := object.(*v1beta1.GatewayClass)
			gc.Status = prepareGatewayClassStatus(*statuses.GatewayClassStatus, upd.cfg.Clock.Now())
		})
	}

	if statuses.GatewayStatus != nil {
		upd.update(ctx, statuses.GatewayStatus.NsName, &v1beta1.Gateway{}, func(object client.Object) {
			gw := object.(*v1beta1.Gateway)
			gw.Status = prepareGatewayStatus(*statuses.GatewayStatus, upd.cfg.Clock.Now())
		})
	}

	for nsname, gs := range statuses.IgnoredGatewayStatuses {
		select {
		case <-ctx.Done():
			return
		default:
		}

		upd.update(ctx, nsname, &v1beta1.Gateway{}, func(object client.Object) {
			gw := object.(*v1beta1.Gateway)
			gw.Status = prepareIgnoredGatewayStatus(gs, upd.cfg.Clock.Now())
		})
	}

	for nsname, rs := range statuses.HTTPRouteStatuses {
		select {
		case <-ctx.Done():
			return
		default:
		}

		upd.update(ctx, nsname, &v1beta1.HTTPRoute{}, func(object client.Object) {
			hr := object.(*v1beta1.HTTPRoute)
			// statuses.GatewayStatus is never nil when len(statuses.HTTPRouteStatuses) > 0
			hr.Status = prepareHTTPRouteStatus(rs, statuses.GatewayStatus.NsName, upd.cfg.GatewayCtlrName, upd.cfg.Clock.Now())
		})
	}
}

func (upd *updaterImpl) update(ctx context.Context, nsname types.NamespacedName, obj client.Object, statusSetter func(client.Object)) {
	// The function handles errors by reporting them in the logs.
	// FIXME(pleshakov): figure out appropriate log level for these errors. Perhaps 3?

	// We need to get the latest version of the resource.
	// Otherwise, the Update status API call can fail.
	// Note: the default client uses a cache for reads, so we're not making an unnecessary API call here.
	// the default is configurable in the Manager options.
	err := upd.cfg.Client.Get(ctx, nsname, obj)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			upd.cfg.Logger.Error(err, "Failed to get the recent version the resource when updating status",
				"namespace", nsname.Namespace,
				"name", nsname.Name,
				"kind", obj.GetObjectKind().GroupVersionKind().Kind)
		}
		return
	}

	statusSetter(obj)

	err = upd.cfg.Client.Status().Update(ctx, obj)
	if err != nil {
		upd.cfg.Logger.Error(err, "Failed to update status",
			"namespace", nsname.Namespace,
			"name", nsname.Name,
			"kind", obj.GetObjectKind().GroupVersionKind().Kind)
	}
}
