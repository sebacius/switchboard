package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/sebas/switchboard/internal/signaling/api"
	"github.com/sebas/switchboard/internal/signaling/b2bua"
	"github.com/sebas/switchboard/internal/signaling/config"
	"github.com/sebas/switchboard/internal/signaling/dialplan"
	"github.com/sebas/switchboard/internal/signaling/dialog"
	"github.com/sebas/switchboard/internal/signaling/location"
	"github.com/sebas/switchboard/internal/signaling/mediaclient"
	"github.com/sebas/switchboard/internal/signaling/routing"
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
		ua.Close()
		return nil, fmt.Errorf("failed to create server: %w", err)
	}
	uac, err := sipgo.NewClient(ua)
	if err != nil {
		ua.Close()
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
	slog.Info("Connecting to RTP Manager pool", "addresses", cfg.RTPManagerAddrs)
	poolCfg := mediaclient.PoolConfig{
		Addresses:           cfg.RTPManagerAddrs,
		ConnectTimeout:      cfg.GRPCConnectTimeout,
		KeepaliveInterval:   cfg.GRPCKeepaliveInterval,
		KeepaliveTimeout:    cfg.GRPCKeepaliveTimeout,
		HealthCheckInterval: 5 * time.Second,
		UnhealthyThreshold:  3,
		HealthyThreshold:    2,
	}
	mediaTransport, err := mediaclient.NewPool(poolCfg)
	if err != nil {
		ua.Close()
		locStore.Close()
		return nil, fmt.Errorf("failed to create RTP Manager pool: %w", err)
	}

	// Create dialog manager (single source of truth for call state)
	dialogMgr := dialog.NewManager(uac, dialogUA)

	// Create API server with register handler and dialog manager
	apiServer := api.NewServer("0.0.0.0:8080", registerHandler, dialogMgr)

	// Load dialplan configuration
	dialplanPath := cfg.DialplanPath
	if dialplanPath == "" {
		dialplanPath = "dialplan.json"
	}
	dp, err := dialplan.New(dialplanPath, slog.Default())
	if err != nil {
		ua.Close()
		locStore.Close()
		mediaTransport.Close()
		return nil, fmt.Errorf("failed to load dialplan: %w", err)
	}
	slog.Info("Dialplan loaded", "path", dialplanPath, "routes", dp.RouteCount())

	// Create dialplan executor with default actions
	executor := dialplan.NewExecutor(dp, dialplan.DefaultRegistry(), slog.Default())

	// Create B2BUA CallService for dial actions
	callService := b2bua.NewCallService(b2bua.CallServiceConfig{
		Client:        uac,
		Resolver:      b2bua.DefaultResolver(locStore, cfg.AdvertiseAddr),
		DialogManager: dialogMgr,
		Transport:     mediaTransport,
		LocalContact:  fmt.Sprintf("sip:switchboard@%s:%d", cfg.AdvertiseAddr, cfg.Port),
		AdvertiseAddr: cfg.AdvertiseAddr,
		Port:          cfg.Port,
	})

	// Create SIP method handlers
	inviteHandler := routing.NewInviteHandler(
		mediaTransport,
		cfg.AdvertiseAddr,
		cfg.Port,
		dialogMgr,
		apiServer,
		executor,
		locStore,
		callService,
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

	// Set up dialog termination callback to cleanup transport sessions and API records
	dialogMgr.SetOnTerminated(func(d *dialog.Dialog) {
		// Remove session from API records
		apiServer.RemoveSession(d.CallID)

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
			p.dialogMgr.Terminate(dlg.CallID, dialog.ReasonLocalBYE)
		}
	}

	// Close dialog manager (stops TTLStore cleanup goroutine)
	if p.dialogMgr != nil {
		p.dialogMgr.Close()
	}

	// Close transport
	if p.transport != nil {
		p.transport.Close()
	}

	// Close location store
	if p.locationStore != nil {
		p.locationStore.Close()
	}

	if p.apiServer != nil {
		p.apiServer.Stop()
	}
	if p.ua != nil {
		return p.ua.Close()
	}
	return nil
}
