package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSecretsAreRead(t *testing.T) {
	root := project(t, map[string]string{
		FileName: "web: {service: web, port: 80}\nenv:\n  set: {NODE_ENV: development}\n  secrets:\n    STRIPE_KEY: op://Dev/Stripe/key\n    DB_PASSWORD: \"vault://secret/shop/db#password\"\n",
	})
	c, err := Load(filepath.Join(root, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if c.Env.Secrets["STRIPE_KEY"] != "op://Dev/Stripe/key" || c.Env.Secrets["DB_PASSWORD"] != "vault://secret/shop/db#password" {
		t.Errorf("secrets = %q", c.Env.Secrets)
	}
}

func TestSecretsAreChecked(t *testing.T) {
	for name, tc := range map[string]struct{ yaml, want string }{
		"no store": {
			"web: {service: web, port: 80}\nenv:\n  secrets:\n    STRIPE_KEY: sk_live_123\n",
			`env.secrets.STRIPE_KEY: "sk_live_123" names no store pit knows`,
		},
		"a broken reference": {
			"web: {service: web, port: 80}\nenv:\n  secrets:\n    DB: vault://secret/db\n",
			"env.secrets.DB: \"vault://secret/db\" is not a Vault reference",
		},
		"not a name": {
			"web: {service: web, port: 80}\nenv:\n  secrets:\n    stripe-key: op://Dev/Stripe/key\n",
			"env.secrets.stripe-key: is not a name a variable can have",
		},
		"set twice": {
			"web: {service: web, port: 80}\nenv:\n  set: {STRIPE_KEY: x}\n  secrets:\n    STRIPE_KEY: op://Dev/Stripe/key\n",
			"env.secrets.STRIPE_KEY: is set in env.set as well",
		},
		"in Kubernetes": {
			"kubernetes: {manifests: [k8s]}\nweb: {service: web, port: 80}\nenv:\n  secrets:\n    STRIPE_KEY: op://Dev/Stripe/key\n",
			"env.secrets: is set for a project in Kubernetes",
		},
	} {
		err := loadBroken(t, tc.yaml, map[string]string{"k8s/web.yaml": "kind: Deployment\n"})
		if !strings.Contains(err.Error(), tc.want) || !hasLineNumber.MatchString(err.Error()) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// Each wrong one is said, in order.
	err := loadBroken(t, "web: {service: web, port: 80}\nenv:\n  secrets:\n    B: nope\n    A: nope\n", nil)
	if a, b := strings.Index(err.Error(), "env.secrets.A"), strings.Index(err.Error(), "env.secrets.B"); a < 0 || b < a {
		t.Errorf("err = %v", err)
	}
}
