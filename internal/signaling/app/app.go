package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/sebas/switchboard/internal/signaling/agent"
	"github.com/sebas/switchboard/internal/signaling/api"
	"github.com/sebas/switchboard/internal/signaling/b2bua"
	"github.com/sebas/switchboard/internal/signaling/config"
	"github.com/sebas/switchboard/internal/signaling/dialog"
	"github.com/sebas/switchboard/internal/signaling/drain"
	"github.com/sebas/switchboard/internal/signaling/filemanager"
	"github.com/sebas/switchboard/internal/signaling/llm"
	"github.com/sebas/switchboard/internal/signaling/location"
	"github.com/sebas/switchboard/internal/signaling/mediaclient"
	"github.com/sebas/switchboard/internal/signaling/parking"
	"github.com/sebas/switchboard/internal/signaling/routing"
	"github.com/sebas/switchboard/internal/signaling/trunk"
)

type SwitchBoard struct {
	ua              *sipgo.UserAgent
	srv             *sipgo.Server
	client          *sipgo.Client
	config          *config.Config
	apiServer       *api.Server
	locationStore   location.LocationStore
	registerHandler *routing.RegisterHandler
	inviteHandler   *routing.InviteHandler
	byeHandler      *routing.BYEHandler
	ackHandler      *routing.ACKHandler
	cancelHandler   *routing.CANCELHandler
	dialogMgr       dialog.DialogStore
	transport       mediaclient.Transport
	callService     b2bua.CallService
}

func NewServer(cfg *config.Config) (*SwitchBoard, error) {
	// Create SIP user agent, server, and client
	ua, err := sipgo.NewUA()
	if err != nil {
		return nil, fmt.Errorf("failed to create user agent: %w", err)
	}
	uas, err := sipgo.NewServer(ua)
	if err != nil {
		_ = ua.Close()
		return nil, fmt.Errorf("failed to create server: %w", err)
	}
	uac, err := sipgo.NewClient(ua)
	if err != nil {
		_ = ua.Close()
		return nil, fmt.Errorf("failed to create client: %w", err)
	}

	// Create location store with TTL support
	locStoreCfg := location.DefaultStoreConfig()
	locStore := location.NewStore(locStoreCfg)

	// Create REGISTER handler with location store
	realm := cfg.AdvertiseAddr
	if realm == "" {
		realm = "switchboard.local"
	}
	registerHandler := routing.NewRegisterHandler(locStore, realm)

	// Create DialogUA for sipgo dialog management
	contact := sip.ContactHeader{
		Address: sip.Uri{
			Scheme: "sip",
			User:   "switchboard",
			Host:   cfg.AdvertiseAddr,
			Port:   cfg.Port,
		},
	}
	dialogUA := &sipgo.DialogUA{
		Client:     uac,
		ContactHDR: contact,
	}

	// Create RTP Manager pool (gRPC transport)
	poolCfg := mediaclient.PoolConfig{
		ConnectTimeout:      cfg.GRPCConnectTimeout,
		KeepaliveInterval:   cfg.GRPCKeepaliveInterval,
		KeepaliveTimeout:    cfg.GRPCKeepaliveTimeout,
		HealthCheckInterval: 10 * time.Second,
		UnhealthyThreshold:  3,
		HealthyThreshold:    2,
	}
	// Use named format (Kubernetes) if provided, otherwise simple format (systemd/local)
	if len(cfg.RTPManagerNodes) > 0 {
		poolCfg.NodeAddresses = cfg.RTPManagerNodes
		slog.Info("Connecting to RTP Manager pool (named format)", "nodes", cfg.RTPManagerNodes)
	} else {
		poolCfg.Addresses = cfg.RTPManagerAddrs
		slog.Info("Connecting to RTP Manager pool (simple format)", "addresses", cfg.RTPManagerAddrs)
	}
	mediaTransport, err := mediaclient.NewPool(poolCfg)
	if err != nil {
		_ = ua.Close()
		locStore.Close()
		return nil, fmt.Errorf("failed to create RTP Manager pool: %w", err)
	}

	// Create dialog manager (single source of truth for call state)
	dialogMgr := dialog.NewManager(uac, dialogUA)

	// Create API server with register handler, dialog manager, and RTP manager stats
	// Pool implements mediaclient.StatsProvider which satisfies api.RtpManagerProvider
	apiAddr := fmt.Sprintf("%s:%d", cfg.BindAddr, cfg.APIPort)
	apiServer := api.NewServer(apiAddr, registerHandler, dialogMgr, mediaTransport)

	// Create drain migrator and coordinator
	localContact := sip.Uri{
		Scheme: "sip",
		User:   "switchboard",
		Host:   cfg.AdvertiseAddr,
		Port:   cfg.Port,
	}
	migrator := drain.NewMigrator(drain.MigratorConfig{
		Pool:          mediaTransport,
		DialogManager: dialogMgr,
		LocalContact:  localContact,
		Mode:          drain.DrainModeGraceful,
	})
	drainCoordinator := drain.NewCoordinator(mediaTransport, migrator)
	apiServer.SetDrainProvider(drainCoordinator)

	// Create parking service
	parkService := parking.NewService(parking.ServiceConfig{
		Transport: mediaTransport,
		Logger:    slog.Default(),
	})

	// Set parking API provider
	parkAdapter := parking.NewAPIAdapter(parkService)
	apiServer.SetParkProvider(parkAdapter)

	// Load tenant prompts (settings.md + tenants/*.md). They are the supervisor's
	// system instruction AND admission's preflight input: a tenant with no prompt
	// is not admissible, so a load failure here means no calls, not silent
	// unsupervised ones.
	prompts, err := agent.NewPromptStore(cfg.SettingsPath, cfg.TenantsPath)
	if err != nil {
		slog.Warn("Tenant prompts loaded with errors; unloaded tenants will be rejected",
			"settings", cfg.SettingsPath, "tenants", cfg.TenantsPath, "error", err)
	}
	slog.Info("Tenant prompts loaded", "tenants", prompts.Tenants())

	// Load per-tenant routing tables (<tenant>.routing.json). This is the data
	// deterministic resolution routes BY, and the source of the symbolic targets
	// the model may dial — one file so the two cannot disagree. A malformed table
	// IS fatal: starting without it would silently send every call that should
	// have been routed in 0ms to a language model instead.
	routingStore, err := agent.NewRoutingStore(cfg.RoutingPath)
	if err != nil {
		_ = ua.Close()
		locStore.Close()
		_ = mediaTransport.Close()
		return nil, fmt.Errorf("failed to load tenant routing tables: %w", err)
	}
	slog.Info("Tenant routing tables loaded", "path", cfg.RoutingPath, "tenants", routingStore.Tenants())

	// Load Class-of-Service + capacity policy. A missing file is the safe
	// default: no external dialing anywhere, default channel limit per tenant.
	policyCfg, err := agent.LoadPolicyConfig(cfg.PolicyPath)
	if err != nil {
		_ = ua.Close()
		locStore.Close()
		_ = mediaTransport.Close()
		return nil, fmt.Errorf("failed to load policy config: %w", err)
	}
	slog.Info("Policy loaded",
		"path", cfg.PolicyPath,
		"default_channel_limit", policyCfg.DefaultChannelLimit,
		"tenants", len(policyCfg.Tenants),
	)

	// Create the tool-calling LLM client. The supervisor cannot run without one,
	// so a misconfigured provider is a hard failure rather than a silently
	// unsupervised switch.
	//
	// The API key is read here, from the environment, and never travels through
	// cfg: that struct is echoed by the startup banner and passed around freely.
	// For the same reason it is not a flag — flags are visible in ps.
	chatClient, err := llm.New(llm.Config{
		Provider:  cfg.LLMProvider,
		ServerURL: cfg.LLMServerURL,
		Model:     cfg.LLMModel,
		KeepAlive: cfg.LLMKeepAlive,
		APIKey:    os.Getenv("OPENAI_API_KEY"),
		// The transport deadline must outlast the largest turn budget, or it
		// fires first and --first-turn-timeout silently does nothing.
		Timeout: cfg.FirstTurnTimeout + 30*time.Second,
	})
	if err != nil {
		_ = ua.Close()
		locStore.Close()
		_ = mediaTransport.Close()
		return nil, fmt.Errorf("LLM supervisor: %w", err)
	}
	logArgs := []any{"provider", cfg.LLMProvider, "server", cfg.LLMServerURL, "model", cfg.LLMModel}
	if cfg.LLMProvider == llm.ProviderOllama {
		logArgs = append(logArgs, "keep_alive", cfg.LLMKeepAlive)
	}
	slog.Info("LLM supervisor configured", logArgs...)
	if cfg.KeepAliveIgnoredWarning != "" {
		slog.Warn(cfg.KeepAliveIgnoredWarning)
	}

	// Check the model is reachable and pulled, then load it — in the background,
	// because a cold multi-gigabyte load takes minutes and the SIP stack must not
	// wait on it. Nothing here can fail the boot: a deployment whose LLM is down
	// still routes every call its tenant routing tables resolve.
	go llm.ProbeAndWarm(context.Background(), chatClient, cfg.LLMModel, slog.Default())

	// Create file manager for config API
	fileMgr := filemanager.New(filemanager.Config{
		SettingsDir:      cfg.SettingsPath,
		TenantsDir:       cfg.TenantsPath,
		SettingsReloader: filemanager.MultiReloader{prompts, routingStore},
	})
	apiServer.SetFileProvider(fileMgr)

	// Create resolver registry for dial targets
	resolver := b2bua.NewTargetResolver()
	resolver.Register("sip:", b2bua.NewDirectResolver())
	resolver.Register("sips:", b2bua.NewDirectResolver())
	resolver.Register("user/", b2bua.NewUserResolver(locStore, cfg.AdvertiseAddr))
	resolver.SetFallback(b2bua.NewUserResolver(locStore, cfg.AdvertiseAddr))

	// Create B2BUA CallService for dial actions
	callService := b2bua.NewCallService(b2bua.CallServiceConfig{
		Client:        uac,
		Resolver:      resolver,
		DialogManager: dialogMgr,
		Transport:     mediaTransport,
		LocalContact:  fmt.Sprintf("sip:switchboard@%s:%d", cfg.AdvertiseAddr, cfg.Port),
		AdvertiseAddr: cfg.AdvertiseAddr,
		Port:          cfg.Port,
	})

	// Wire BridgeMapper to migrator for bridged call migration during drain
	migrator.SetBridgeMapper(callService.GetBridgeMapper())

	// Load SIP trunk peers and DID->tenant routes
	trunkPeers, err := trunk.LoadPeers(cfg.TrunkConfigPath)
	if err != nil {
		_ = ua.Close()
		locStore.Close()
		_ = mediaTransport.Close()
		return nil, fmt.Errorf("failed to load trunk peers: %w", err)
	}
	didRoutes, err := trunk.LoadRoutes(cfg.RoutesPath)
	if err != nil {
		_ = ua.Close()
		locStore.Close()
		_ = mediaTransport.Close()
		return nil, fmt.Errorf("failed to load routes: %w", err)
	}
	sipTrunk := trunk.NewStaticTrunk(trunkPeers, "")
	slog.Info("Trunk loaded", "peers", trunkPeers.Count(), "dids", didRoutes.Count())

	// Deterministic pre-LLM layers: the router classifies direction and resolves
	// the tenant (no default), and admission enforces preflight + per-tenant
	// channel limits before the INVITE is answered.
	directory := agent.DirectoryFromLocation(locStore)
	callRouter := agent.NewRouter(directory, sipTrunk, didRoutes)
	admission := agent.NewAdmission(prompts, routingStore, policyCfg.DefaultChannelLimit, policyCfg.ChannelLimits())

	// buildPolicy is shared by deterministic resolution and the supervisor so a
	// destination is adjudicated identically however it was chosen. The symbolic
	// targets come from the tenant's routing table — policy.json no longer
	// carries them, and refuses to start if it still does.
	buildPolicy := func(cc agent.CallContext) *agent.Policy {
		return agent.NewPolicy(cc.Tenant,
			policyCfg.TenantPolicyFor(cc.Tenant, agent.SymbolicTargetsFor(routingStore, cc.Tenant)),
			slog.Default())
	}

	// operatorFor is the tenant's fallback human, read from the same routing table
	// the resolver uses. The executor needs it so an unknown tool name transfers
	// the caller instead of hanging up on them.
	operatorFor := func(cc agent.CallContext) string {
		table, ok := routingStore.TenantRouting(cc.Tenant)
		if !ok {
			return ""
		}
		return table.Operator
	}

	// Deterministic resolution runs BEFORE the supervisor. A call with exactly
	// one correct destination — a registered extension, a *7XX pickup, a mapped
	// DID, a ring group — is executed here and never reaches the model.
	callResolution := agent.NewCallResolution(agent.CallResolutionConfig{
		Resolver:    agent.NewResolver(routingStore, directory, parkService, slog.Default()),
		Parking:     parkService,
		BuildPolicy: buildPolicy,
		Logger:      slog.Default(),
	})

	// The supervisor runner. The tool registry is built PER CALL from
	// (tenant, direction) — an inbound caller is never offered external dial —
	// and every consequential call is adjudicated by that tenant's policy.
	runner := agent.NewRunner(agent.RunnerConfig{
		Prompts:          prompts,
		Model:            cfg.LLMModel,
		Voice:            cfg.TTSVoice,
		Chat:             chatClient,
		Logger:           slog.Default(),
		TurnTimeout:      cfg.TurnTimeout,
		FirstTurnTimeout: cfg.FirstTurnTimeout,
		BuildExecutor: func(cc agent.CallContext) agent.ToolExecutor {
			policy := buildPolicy(cc)
			registry := agent.BuildRegistry(cc, policy, agent.RegistryDeps{
				Parking: parkService,
				Logger:  slog.Default(),
			})
			return agent.NewCallExecutor(registry, policy, operatorFor(cc))
		},
	})

	// Create SIP method handlers
	inviteHandler := routing.NewInviteHandler(
		mediaTransport,
		cfg.AdvertiseAddr,
		cfg.Port,
		dialogMgr,
		apiServer,
		callRouter,
		admission,
		callResolution,
		runner,
		locStore,
		callService,
		sipTrunk,
		didRoutes,
	)
	byeHandler := routing.NewBYEHandler(dialogMgr, callService)
	ackHandler := routing.NewACKHandler(dialogMgr)
	cancelHandler := routing.NewCANCELHandler(dialogMgr)

	proxy := &SwitchBoard{
		ua:              ua,
		srv:             uas,
		client:          uac,
		config:          cfg,
		locationStore:   locStore,
		registerHandler: registerHandler,
		apiServer:       apiServer,
		inviteHandler:   inviteHandler,
		byeHandler:      byeHandler,
		ackHandler:      ackHandler,
		cancelHandler:   cancelHandler,
		dialogMgr:       dialogMgr,
		transport:       mediaTransport,
		callService:     callService,
	}

	// Set up dialog termination callback to cleanup transport sessions, API records, and parking
	dialogMgr.SetOnTerminated(func(d *dialog.Dialog) {
		// Remove session from API records
		apiServer.RemoveSession(d.CallID)

		// Cleanup parked call if this was a parked dialog
		parkService.CleanupByCallID(d.CallID)

		if sessionID := d.GetSessionID(); sessionID != "" {
			reason := mediaclient.TerminateReasonNormal
			switch d.TerminateReason {
			case dialog.ReasonRemoteBYE:
				reason = mediaclient.TerminateReasonBYE
			case dialog.ReasonCancel:
				reason = mediaclient.TerminateReasonCancel
			case dialog.ReasonTimeout:
				reason = mediaclient.TerminateReasonTimeout
			case dialog.ReasonError:
				reason = mediaclient.TerminateReasonError
			}
			if err := mediaTransport.DestroySession(context.Background(), sessionID, reason); err != nil {
				slog.Warn("[App] Failed to destroy session", "session_id", sessionID, "error", err)
			}
		}
	})

	// Register request handlers
	uas.OnRequest(sip.REGISTER, proxy.handleRegister)
	uas.OnRequest(sip.INVITE, proxy.handleINVITE)
	uas.OnRequest(sip.BYE, proxy.handleBYE)
	uas.OnRequest(sip.ACK, proxy.handleACK)
	uas.OnRequest(sip.CANCEL, proxy.handleCANCEL)

	slog.Info("SIP handlers registered", "methods", "REGISTER, INVITE, BYE, ACK, CANCEL")
	slog.Info("Configuration", "port", cfg.Port, "bind", cfg.BindAddr, "realm", realm)

	return proxy, nil
}

func (p *SwitchBoard) Start(ctx context.Context) error {
	listenAddr := fmt.Sprintf("%s:%d", p.config.BindAddr, p.config.Port)
	slog.Info("Starting SIP server", "listenAddr", listenAddr)

	// Start API server
	if err := p.apiServer.Start(); err != nil {
		slog.Error("Failed to start API server", "error", err)
		panic(err)
	}

	if err := p.srv.ListenAndServe(ctx, "udp", listenAddr); err != nil {
		slog.Error("Failed to bind to SIP port", "port", p.config.Port, "error", err)
		panic(err)
	}

	return nil
}

func (p *SwitchBoard) handleRegister(req *sip.Request, tx sip.ServerTransaction) {
	if err := p.registerHandler.HandleRegister(req, tx); err != nil {
		slog.Error("Error handling REGISTER", "error", err)
		res := sip.NewResponseFromRequest(req, sip.StatusInternalServerError, "Server Error", nil)
		if err := tx.Respond(res); err != nil {
			slog.Error("Error sending error response", "error", err)
		}
	}
}

func (p *SwitchBoard) handleINVITE(req *sip.Request, tx sip.ServerTransaction) {
	p.inviteHandler.HandleINVITE(req, tx)
}

func (p *SwitchBoard) handleBYE(req *sip.Request, tx sip.ServerTransaction) {
	p.byeHandler.HandleBYE(req, tx)
}

func (p *SwitchBoard) handleACK(req *sip.Request, tx sip.ServerTransaction) {
	p.ackHandler.HandleACK(req, tx)
}

func (p *SwitchBoard) handleCANCEL(req *sip.Request, tx sip.ServerTransaction) {
	p.cancelHandler.HandleCANCEL(req, tx)
}

func (p *SwitchBoard) Close() error {
	// Terminate all active dialogs gracefully
	dialogs := p.dialogMgr.List()
	for _, dlg := range dialogs {
		if !dlg.IsTerminated() {
			_ = p.dialogMgr.Terminate(dlg.CallID, dialog.ReasonLocalBYE)
		}
	}

	// Close dialog manager (stops TTLStore cleanup goroutine)
	if p.dialogMgr != nil {
		p.dialogMgr.Close()
	}

	// Close transport
	if p.transport != nil {
		_ = p.transport.Close()
	}

	// Close location store
	if p.locationStore != nil {
		p.locationStore.Close()
	}

	if p.apiServer != nil {
		_ = p.apiServer.Stop()
	}
	if p.ua != nil {
		return p.ua.Close()
	}
	return nil
}
