package writetrace

import "strings"

type commitProgressConfig struct {
	repo     string
	systems  []string
	total    int
	interval int
	logf     func(string, ...any)
}

type commitProgressLogger struct {
	cfg        commitProgressConfig
	lastLogged int
}

func newCommitProgressLogger(cfg commitProgressConfig) *commitProgressLogger {
	return &commitProgressLogger{cfg: cfg}
}

func (p *commitProgressLogger) Observe(replayed int) {
	if p == nil || p.cfg.interval <= 0 || p.cfg.total <= 0 || p.cfg.logf == nil {
		return
	}
	if replayed < p.cfg.total && replayed%p.cfg.interval != 0 {
		return
	}
	p.log(replayed)
}

func (p *commitProgressLogger) Complete() {
	if p == nil || p.cfg.interval <= 0 || p.cfg.total <= 0 || p.cfg.logf == nil {
		return
	}
	p.log(p.cfg.total)
}

func (p *commitProgressLogger) log(replayed int) {
	if replayed <= p.lastLogged {
		return
	}
	if replayed > p.cfg.total {
		replayed = p.cfg.total
	}
	percent := float64(replayed) / float64(p.cfg.total) * 100
	p.cfg.logf("  progress repository=%s systems=%s replayed=%d/%d percent=%.2f%%",
		p.cfg.repo,
		strings.Join(p.cfg.systems, ","),
		replayed,
		p.cfg.total,
		percent)
	p.lastLogged = replayed
}
