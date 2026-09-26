package kube

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
)

// kustomizations are the names kustomize reads a directory by.
var kustomizations = []string{"kustomization.yaml", "kustomization.yml", "Kustomization"}

// IsKustomization says whether dir is one kustomize builds.
func IsKustomization(dir string) bool {
	for _, name := range kustomizations {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}

// Resources are the manifests' paths as kustomize takes them: a file,
// a directory kustomize builds, or each YAML file of any other
// directory.
func Resources(paths []string) ([]string, error) {
	var out []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, errs.Wrap(err, "cannot read the manifests at %s", p)
		}
		if !info.IsDir() || IsKustomization(p) {
			out = append(out, p)
			continue
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			return nil, errs.Wrap(err, "cannot read the manifests at %s", p)
		}
		before := len(out)
		for _, e := range entries {
			ext := filepath.Ext(e.Name())
			if !e.IsDir() && (ext == ".yaml" || ext == ".yml") {
				out = append(out, filepath.Join(p, e.Name()))
			}
		}
		if len(out) == before {
			return nil, errs.New("%s has no manifests: no YAML files, and no kustomization", p)
		}
	}
	return out, nil
}

// WriteKustomization writes the kustomization pit renders a sandbox
// from, in dir: the project's manifests, in the sandbox's namespace,
// with the images pit built for it.
func WriteKustomization(dir, namespace string, resources []string, images []Image) error {
	type newImage struct {
		Name    string `yaml:"name"`
		NewName string `yaml:"newName"`
		NewTag  string `yaml:"newTag"`
	}
	k := struct {
		APIVersion string     `yaml:"apiVersion"`
		Kind       string     `yaml:"kind"`
		Namespace  string     `yaml:"namespace"`
		Resources  []string   `yaml:"resources"`
		Images     []newImage `yaml:"images,omitempty"`
	}{APIVersion: "kustomize.config.k8s.io/v1beta1", Kind: "Kustomization", Namespace: namespace, Resources: resources}
	for _, im := range images {
		name, tag := splitTag(im.Tag)
		k.Images = append(k.Images, newImage{Name: im.Name, NewName: name, NewTag: tag})
	}
	data, err := yaml.Marshal(k)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return errs.Wrap(err, "cannot create %s", dir)
	}
	path := filepath.Join(dir, "kustomization.yaml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return errs.Wrap(err, "cannot write %s", path)
	}
	return nil
}

// splitTag separates an image's tag from its name. A registry's port
// is not a tag: localhost:5000/web has none.
func splitTag(image string) (name, tag string) {
	i := strings.LastIndex(image, ":")
	if i < 0 || strings.Contains(image[i:], "/") {
		return image, "latest"
	}
	return image[:i], image[i+1:]
}

// Render builds a kustomization into the manifests it stands for. The
// project's own manifests are outside pit's directory, which kustomize
// allows only when told to.
func Render(ctx context.Context, r Runner, dir string) ([]byte, error) {
	out, err := r.Output(ctx, proc.Command{Name: "kubectl", Args: []string{"kustomize", "--load-restrictor", "LoadRestrictionsNone", dir}})
	if err != nil {
		return nil, errs.Wrap(err, "cannot render the manifests").
			WithHint("`kubectl kustomize --load-restrictor LoadRestrictionsNone %s` reproduces it", dir)
	}
	return out, nil
}

// Workload is one of the manifests' Deployments, StatefulSets and
// DaemonSets: what pit calls a service.
type Workload struct {
	// Kind is kubectl's word for it, as in deployment/web.
	Kind string
	Name string
	// Containers are what it runs.
	Containers []Container
}

// Ref is how kubectl names it.
func (w Workload) Ref() string { return w.Kind + "/" + w.Name }

// Container is one container of a workload.
type Container struct {
	Name       string
	Image      string
	PullPolicy string
}

// workloadKinds are the kinds that run something a reviewer can open,
// read the output of and get a shell in.
var workloadKinds = map[string]string{"Deployment": "deployment", "StatefulSet": "statefulset", "DaemonSet": "daemonset"}

// Workloads reads the workloads of rendered manifests, in their order.
func Workloads(rendered []byte) ([]Workload, error) {
	type container struct {
		Name            string `yaml:"name"`
		Image           string `yaml:"image"`
		ImagePullPolicy string `yaml:"imagePullPolicy"`
	}
	type doc struct {
		Kind     string `yaml:"kind"`
		Metadata struct {
			Name string `yaml:"name"`
		} `yaml:"metadata"`
		Spec struct {
			Template struct {
				Spec struct {
					InitContainers []container `yaml:"initContainers"`
					Containers     []container `yaml:"containers"`
				} `yaml:"spec"`
			} `yaml:"template"`
		} `yaml:"spec"`
	}
	var out []Workload
	dec := yaml.NewDecoder(bytes.NewReader(rendered))
	for {
		var d doc
		err := dec.Decode(&d)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, errs.Wrap(err, "cannot read the rendered manifests")
		}
		kind, ok := workloadKinds[d.Kind]
		if !ok {
			continue
		}
		w := Workload{Kind: kind, Name: d.Metadata.Name}
		for _, c := range slices.Concat(d.Spec.Template.Spec.InitContainers, d.Spec.Template.Spec.Containers) {
			w.Containers = append(w.Containers, Container{Name: c.Name, Image: c.Image, PullPolicy: c.ImagePullPolicy})
		}
		out = append(out, w)
	}
	return out, nil
}

// CheckPulls finds a container that would pull an image pit built from
// a registry, where it is not: pit loads its images into the cluster,
// and a pull policy of Always ignores them.
func CheckPulls(workloads []Workload, images []Image) error {
	for _, w := range workloads {
		for _, c := range w.Containers {
			for _, im := range images {
				if c.Image == im.Tag && c.PullPolicy == "Always" {
					return errs.New("%s pulls %s always, and pit's image of it is in the cluster, not in a registry", w.Ref(), im.Name).
						WithHint("set imagePullPolicy to IfNotPresent for its container %s, or leave it out", c.Name)
				}
			}
		}
	}
	return nil
}

// ReadWorkloads reads the workloads of the manifests at paths as they
// are, without pit's namespace or images: which ones there are.
func ReadWorkloads(ctx context.Context, r Runner, paths []string) ([]Workload, error) {
	resources, err := Resources(paths)
	if err != nil {
		return nil, err
	}
	var out []Workload
	for _, res := range resources {
		var data []byte
		if IsKustomization(res) {
			data, err = Render(ctx, r, res)
		} else {
			data, err = os.ReadFile(res)
		}
		if err != nil {
			return nil, err
		}
		ws, err := Workloads(data)
		if err != nil {
			return nil, errs.Wrap(err, "%s", res)
		}
		out = append(out, ws...)
	}
	return out, nil
}
