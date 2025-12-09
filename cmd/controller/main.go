package main

import (
	"flag"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	karpetrackv1alpha1 "github.com/maccam912/karpetrack/api/v1alpha1"
	"github.com/maccam912/karpetrack/internal/controller"
	"github.com/maccam912/karpetrack/internal/spot"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(karpetrackv1alpha1.AddToScheme(scheme))
}

type Config struct {
	// Metrics and health
	MetricsAddr          string
	ProbeAddr            string
	EnableLeaderElection bool

	// Rackspace Spot
	RefreshToken string
	CloudspaceID string
	MockMode     bool

	// Controller settings
	PricingRefreshInterval time.Duration
	OptimizationThreshold  float64
	ConsolidationEnabled   bool
	BatchWindow            time.Duration
	EmptyNodeGracePeriod   time.Duration
}

func main() {
	var config Config

	// Controller flags
	flag.StringVar(&config.MetricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&config.ProbeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&config.EnableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")

	// Rackspace Spot flags
	flag.StringVar(&config.RefreshToken, "refresh-token", "", "Rackspace Spot refresh token (or set RACKSPACE_SPOT_REFRESH_TOKEN)")
	flag.StringVar(&config.CloudspaceID, "cloudspace-id", "", "Rackspace Spot cloudspace ID (or set RACKSPACE_SPOT_CLOUDSPACE_ID)")
	flag.BoolVar(&config.MockMode, "mock-mode", false, "Enable mock mode for testing without real Spot API")

	// Controller behavior flags
	flag.DurationVar(&config.PricingRefreshInterval, "pricing-refresh-interval", 30*time.Second, "How often to refresh pricing data")
	flag.Float64Var(&config.OptimizationThreshold, "optimization-threshold", 0.20, "Minimum savings percentage to trigger node replacement")
	flag.BoolVar(&config.ConsolidationEnabled, "consolidation-enabled", true, "Enable node consolidation")
	flag.DurationVar(&config.BatchWindow, "batch-window", 10*time.Second, "How long to batch pending pods before provisioning")
	flag.DurationVar(&config.EmptyNodeGracePeriod, "empty-node-grace-period", 5*time.Minute, "How long a node must be empty before removal")

	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// Get credentials from environment if not set via flags
	if config.RefreshToken == "" {
		config.RefreshToken = os.Getenv("RACKSPACE_SPOT_REFRESH_TOKEN")
	}
	if config.CloudspaceID == "" {
		config.CloudspaceID = os.Getenv("RACKSPACE_SPOT_CLOUDSPACE_ID")
	}

	// Validate configuration
	if !config.MockMode {
		if config.RefreshToken == "" {
			setupLog.Error(nil, "Rackspace Spot refresh token is required")
			os.Exit(1)
		}
		if config.CloudspaceID == "" {
			setupLog.Error(nil, "Rackspace Spot cloudspace ID is required")
			os.Exit(1)
		}
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: config.MetricsAddr,
		},
		HealthProbeBindAddress: config.ProbeAddr,
		LeaderElection:         config.EnableLeaderElection,
		LeaderElectionID:       "karpetrack-leader-election",
	})
	if err != nil {
		setupLog.Error(err, "unable to create manager")
		os.Exit(1)
	}

	// Create Spot client
	spotClient, err := spot.NewClient(spot.ClientConfig{
		RefreshToken: config.RefreshToken,
		CloudspaceID: config.CloudspaceID,
		MockMode:     config.MockMode,
	})
	if err != nil {
		setupLog.Error(err, "unable to create Spot client")
		os.Exit(1)
	}

	// Create event recorder
	recorder := mgr.GetEventRecorderFor("karpetrack")

	// Setup Provisioner controller
	provisionerController := controller.NewProvisionerController(
		mgr.GetClient(),
		spotClient,
		recorder,
	)
	provisionerController.BatchWindow = config.BatchWindow
	if err := provisionerController.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Provisioner")
		os.Exit(1)
	}

	// Setup SpotNode controller
	spotNodeController := controller.NewSpotNodeController(
		mgr.GetClient(),
		spotClient,
		recorder,
	)
	if err := spotNodeController.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "SpotNode")
		os.Exit(1)
	}

	// Setup Optimizer controller (price monitoring)
	optimizerController := controller.NewOptimizerController(
		mgr.GetClient(),
		spotClient,
		recorder,
	)
	optimizerController.Threshold = config.OptimizationThreshold
	optimizerController.CheckInterval = config.PricingRefreshInterval
	if err := optimizerController.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Optimizer")
		os.Exit(1)
	}

	// Setup Scaler controller (scale down)
	if config.ConsolidationEnabled {
		scalerController := controller.NewScalerController(
			mgr.GetClient(),
			recorder,
		)
		scalerController.EmptyNodeGracePeriod = config.EmptyNodeGracePeriod
		if err := scalerController.SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "Scaler")
			os.Exit(1)
		}
	}

	// Setup health checks
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager",
		"mockMode", config.MockMode,
		"optimizationThreshold", config.OptimizationThreshold,
		"pricingRefreshInterval", config.PricingRefreshInterval,
		"consolidationEnabled", config.ConsolidationEnabled,
	)

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
