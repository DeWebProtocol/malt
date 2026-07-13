package server

import (
	"fmt"
	"sync"

	malt "github.com/dewebprotocol/malt"
	authverifier "github.com/dewebprotocol/malt/auth/verifier"
)

// portableVerifierCache initializes the verification-only commitment
// registry at most once for a Server. Constructing the built-in KZG and IPA
// backends is intentionally deferred until after a handler has bounded,
// decoded, and validated its request body.
type portableVerifierCache struct {
	once     sync.Once
	factory  func() (malt.ProofVerifier, error)
	verifier malt.ProofVerifier
	err      error
}

func (c *portableVerifierCache) load() (malt.ProofVerifier, error) {
	c.once.Do(func() {
		factory := c.factory
		if factory == nil {
			factory = newPortableVerifier
		}
		c.verifier, c.err = factory()
		if c.err == nil && c.verifier == nil {
			c.err = fmt.Errorf("portable verifier factory returned nil")
		}
	})
	return c.verifier, c.err
}

func newPortableVerifier() (malt.ProofVerifier, error) {
	return authverifier.NewDefault()
}
