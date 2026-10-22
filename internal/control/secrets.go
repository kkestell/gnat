package control

import (
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
)

// SetSecrets gives each agent the secret it proves who it is with. Only hashes are kept.
// An agent without a secret, or with an empty one, has none to present, and can do nothing
// that needs one.
func (c *Control) SetSecrets(secrets map[string]string) error {
	hashes := map[string][sha256.Size]byte{}
	for agent, secret := range secrets {
		if c.mailboxes[agent] == nil {
			return fmt.Errorf("a secret for '%s', which is not an agent of this swarm", agent)
		}
		if secret != "" {
			hashes[agent] = sha256.Sum256([]byte(secret))
		}
	}
	c.secrets = hashes
	return nil
}

// whoIs is the agent the secret belongs to, or "" if it is nobody's. It compares in
// constant time with every agent's, so how long it takes says nothing about which agent
// came closest.
func (c *Control) whoIs(secret string) string {
	if secret == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(secret))
	found := ""
	for agent, hash := range c.secrets {
		if subtle.ConstantTimeCompare(sum[:], hash[:]) == 1 {
			found = agent
		}
	}
	return found
}
