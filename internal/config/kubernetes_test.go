package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestKubernetesTakesThePlaceOfComposeFiles(t *testing.T) {
	root := project(t, map[string]string{
		FileName:             "kubernetes:\n  manifests: [k8s]\n  images:\n    - name: shop-web\n    - {name: shop-api, context: api, dockerfile: Dockerfile.dev}\nweb: {service: web, port: 8080}\n",
		"k8s/web.yaml":       "kind: Deployment\n",
		"Dockerfile":         "FROM scratch\n",
		"api/Dockerfile.dev": "FROM scratch\n",
	})
	c, err := Load(filepath.Join(root, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Compose.Files) != 0 || !c.Kubernetes.On() || len(c.Kubernetes.Images) != 2 {
		t.Fatalf("config = %+v", c)
	}
	// An image's context is the repository's root when left out.
	if im := c.Kubernetes.Images[0]; im.Context != "." || im.Dockerfile != "" {
		t.Errorf("image = %+v", im)
	}
}

func TestKubernetesIsChecked(t *testing.T) {
	files := map[string]string{"k8s/web.yaml": "kind: Deployment\n", "Dockerfile": "FROM scratch\n", "api/Dockerfile": "FROM scratch\n"}
	for name, tc := range map[string]struct{ yaml, want string }{
		"no manifests there": {
			"kubernetes: {manifests: [deploy]}\nweb: {service: web, port: 80}\n",
			`kubernetes.manifests[0]: names "deploy", which does not exist`,
		},
		"with compose files": {
			"kubernetes: {manifests: [k8s]}\ncompose: {files: [docker-compose.yml]}\nweb: {service: web, port: 80}\n",
			"kubernetes.manifests: is set together with compose.files",
		},
		"images without manifests": {
			"kubernetes: {images: [{name: web}]}\nweb: {service: web, port: 80}\n",
			"kubernetes.images: is set without kubernetes.manifests",
		},
		"an image without a name": {
			"kubernetes: {manifests: [k8s], images: [{context: .}]}\nweb: {service: web, port: 80}\n",
			"kubernetes.images[0].name: is not set",
		},
		"an image twice": {
			"kubernetes: {manifests: [k8s], images: [{name: web}, {name: web, context: api}]}\nweb: {service: web, port: 80}\n",
			`kubernetes.images[1].name: names "web" a second time`,
		},
		"a context that is not there": {
			"kubernetes: {manifests: [k8s], images: [{name: web, context: frontend}]}\nweb: {service: web, port: 80}\n",
			`kubernetes.images[0].context: names "frontend", which is not a directory`,
		},
		"no Dockerfile": {
			"kubernetes: {manifests: [k8s], images: [{name: web, dockerfile: Dockerfile.prod}]}\nweb: {service: web, port: 80}\n",
			"kubernetes.images[0].dockerfile: . has no Dockerfile.prod",
		},
		"no port": {
			"kubernetes: {manifests: [k8s]}\nweb: {service: web, port: 0}\n",
			"web.port: is 0",
		},
		"a compose command": {
			"kubernetes: {manifests: [k8s]}\nweb: {service: web, port: 80}\ndata:\n  migrate: [\"compose exec -T db psql\"]\n",
			"data.migrate[0]: runs through compose, and the services are Kubernetes workloads",
		},
	} {
		err := loadBroken(t, tc.yaml, files)
		if !strings.Contains(err.Error(), tc.want) || !hasLineNumber.MatchString(err.Error()) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// kubectl is how a command reaches the sandbox, and needs no more.
	root := project(t, map[string]string{
		FileName:       "kubernetes: {manifests: [k8s]}\nweb: {service: web, port: 80}\ndata:\n  migrate: [\"kubectl exec deploy/db -- psql\"]\n",
		"k8s/web.yaml": "kind: Deployment\n",
	})
	if _, err := Load(filepath.Join(root, FileName)); err != nil {
		t.Error(err)
	}
}
