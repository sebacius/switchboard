package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/sebas/switchboard/services/signaling/api"
	"github.com/sebas/switchboard/services/signaling/config"
	"github.com/sebas/switchboard/services/signaling/dialog"
	"github.com/sebas/switchboard/services/signaling/registration"
	"github.com/sebas/switchboard/services/signaling/routing"
	"github.com/sebas/switchboard/services/signaling/transport"
)

type SwitchBoard struct {
	ua              *sipgo.UserAgent
	srv             *sipgo.Server
	client          *sipgo.Client
	config          *config.Config
	apiServer       *api.Server
	registrationMgr *registration.Handler
	inviteHandler   *routing.InviteHandler
	dialogMgr       *dialog.Manager
	transport       transport.Transport
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

	// Create registration handler
	regStore := registration.NewStore()
	regHandler := registration.NewHandler(regStore)

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
	poolCfg := transport.PoolConfig{
		Addresses:           cfg.RTPManagerAddrs,
		ConnectTimeout:      cfg.GRPCConnectTimeout,
		KeepaliveInterval:   cfg.GRPCKeepaliveInterval,
		KeepaliveTimeout:    cfg.GRPCKeepaliveTimeout,
		HealthCheckInterval: 5 * time.Second,
		UnhealthyThreshold:  3,
		HealthyThreshold:    2,
	}
	mediaTransport, err := transport.NewPool(poolCfg)
	if err != nil {
		ua.Close()
		return nil, fmt.Errorf("failed to create RTP Manager pool: %w", err)
	}

	// Create dialog manager (single source of truth for call state)
	dialogMgr := dialog.NewManager(uac, dialogUA)

	// Create INVITE handler
	inviteHandler := routing.NewInviteHandler(
		mediaTransport,
		cfg.AdvertiseAddr,
		cfg.Port,
		"audio/demo-congrats.wav",
		dialogMgr,
	)

	// Create API server
	apiServer := api.NewServer(":8080", regHandler)

	proxy := &SwitchBoard{
		ua:              ua,
		srv:             uas,
		client:          uac,
		config:          cfg,
		registrationMgr: regHandler,
		apiServer:       apiServer,
		inviteHandler:   inviteHandler,
		dialogMgr:       dialogMgr,
		transport:       mediaTransport,
	}

	// Set up dialog termination callback to cleanup transport sessions
	dialogMgr.SetOnTerminated(func(d *dialog.Dialog) {
		if sessionID := d.GetSessionID(); sessionID != "" {
			reason := transport.TerminateReasonNormal
			switch d.TerminateReason {
			case dialog.ReasonRemoteBYE:
				reason = transport.TerminateReasonBYE
			case dialog.ReasonCancel:
				reason = transport.TerminateReasonCancel
			case dialog.ReasonTimeout:
				reason = transport.TerminateReasonTimeout
			case dialog.ReasonError:
				reason = transport.TerminateReasonError
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
	slog.Info("Configuration", "port", cfg.Port, "bind", cfg.BindAddr)

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
	if err := p.registrationMgr.HandleRegister(req, tx); err != nil {
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
	if err := p.dialogMgr.HandleIncomingBYE(req, tx); err != nil {
		slog.Debug("[App] BYE handling note", "error", err)
	}
}

func (p *SwitchBoard) handleACK(req *sip.Request, tx sip.ServerTransaction) {
	if err := p.dialogMgr.ConfirmWithACK(req, tx); err != nil {
		slog.Debug("[App] ACK handling note", "error", err)
	}
}

func (p *SwitchBoard) handleCANCEL(req *sip.Request, tx sip.ServerTransaction) {
	if err := p.dialogMgr.HandleIncomingCANCEL(req, tx); err != nil {
		slog.Debug("[App] CANCEL handling note", "error", err)
	}
}

func (p *SwitchBoard) Close() error {
	// Terminate all active dialogs gracefully
	dialogs := p.dialogMgr.List()
	for _, dlg := range dialogs {
		if !dlg.IsTerminated() {
			p.dialogMgr.Terminate(dlg.CallID, dialog.ReasonLocalBYE)
		}
	}

	// Close transport
	if p.transport != nil {
		p.transport.Close()
	}

	if p.apiServer != nil {
		p.apiServer.Stop()
	}
	if p.ua != nil {
		return p.ua.Close()
	}
	return nil
}
