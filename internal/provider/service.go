package provider

import (
	"context"
	"sync"
	"time"

	"github.com/nyanjou/cliproxyapi-cursor-plugin/internal/transport"
)

const providerID = "cursor"

type outputHost interface {
	Emit(ctx context.Context, streamID string, payload []byte) error
	CloseOutput(ctx context.Context, streamID, message string)
}

type Service struct {
	host transport.Host
	now  func() time.Time

	configMu sync.RWMutex
	config   Config
	sem      chan struct{}

	modelMu      sync.Mutex
	modelExpires time.Time
	modelsCache  []cursorModel

	loginMu sync.Mutex
	logins  map[string]*loginSession
}

type loginSession struct {
	startedAt time.Time
	expiresAt time.Time
	output    string
	done      bool
}

func New(host transport.Host) *Service {
	cfg := DefaultConfig()
	return &Service{
		host:   host,
		now:    time.Now,
		config: cfg,
		sem:    make(chan struct{}, cfg.MaxConcurrent),
		logins: make(map[string]*loginSession),
	}
}

func (s *Service) Configure(raw []byte) error {
	cfg, err := ParseConfig(raw)
	if err != nil {
		return err
	}
	s.configMu.Lock()
	s.config = cfg
	s.sem = make(chan struct{}, cfg.MaxConcurrent)
	s.configMu.Unlock()
	s.modelMu.Lock()
	s.modelsCache = nil
	s.modelExpires = time.Time{}
	s.modelMu.Unlock()
	if cfg.Enabled {
		_ = ensureWorkspace(cfg.Workspace)
	}
	return nil
}

func (s *Service) Config() Config { s.configMu.RLock(); defer s.configMu.RUnlock(); return s.config }

func (s *Service) Shutdown() {
	s.modelMu.Lock()
	s.modelsCache = nil
	s.modelExpires = time.Time{}
	s.modelMu.Unlock()
	s.loginMu.Lock()
	clear(s.logins)
	s.loginMu.Unlock()
}
