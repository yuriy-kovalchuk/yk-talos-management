package run

import (
	"flag"
	"fmt"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/yuriy-kovalchuk/yk-talos-management/api/v1alpha1"
	"github.com/yuriy-kovalchuk/yk-talos-management/internal/controller"
	"github.com/yuriy-kovalchuk/yk-talos-management/internal/version"
)

func Run() error {
	var zapOpts zap.Options
	zapOpts.BindFlags(flag.CommandLine)
	leaderElect := flag.Bool("leader-elect", isInCluster(), "Enable leader election for high availability")
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))

	opts := ctrl.Options{
		Scheme:                 buildScheme(),
		Metrics:                server.Options{BindAddress: ":8080"},
		HealthProbeBindAddress: ":8081",
	}

	if *leaderElect {
		opts.LeaderElection = true
		opts.LeaderElectionID = "yk-talos-management.lock"
		opts.LeaderElectionNamespace = podNamespace()
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), opts)
	if err != nil {
		return fmt.Errorf("create manager: %w", err)
	}

	if err := setupControllers(mgr); err != nil {
		return err
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("add healthz check: %w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("add readyz check: %w", err)
	}

	log.Log.Info("YK Talos Management started",
		"version", version.Version,
		"commit", version.Commit,
		"buildDate", version.BuildDate,
		"goVersion", version.GoVersion(),
	)

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		return fmt.Errorf("manager exited: %w", err)
	}

	return nil
}

func buildScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(v1alpha1.AddToScheme(s))
	return s
}

func isInCluster() bool {
	_, host := os.LookupEnv("KUBERNETES_SERVICE_HOST")
	_, port := os.LookupEnv("KUBERNETES_PORT")
	return host && port
}

// podNamespace returns the namespace the operator is running in.
// Populated by the Downward API via POD_NAMESPACE; falls back to "default".
func podNamespace() string {
	if ns := os.Getenv("POD_NAMESPACE"); ns != "" {
		return ns
	}
	return "default"
}

func setupControllers(mgr ctrl.Manager) error {
	if err := (&controller.TalosClusterReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("taloscluster-controller"),
	}).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := (&controller.TalosNodeReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Talos:    controller.RealDialer{},
		Recorder: mgr.GetEventRecorderFor("talosnode-controller"),
	}).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := (&controller.TalosClusterBootstrapReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Talos:    controller.RealDialer{},
		Recorder: mgr.GetEventRecorderFor("talosclusterbootstrap-controller"),
	}).SetupWithManager(mgr); err != nil {
		return err
	}
	return nil
}

