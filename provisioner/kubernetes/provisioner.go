package kubernetes

import (
	"context"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"nix-builder-provisioner/provisioner"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Config holds Kubernetes-specific provisioner settings.
type Config struct {
	Namespace       string // default: "nix-builders"
	BuilderImage    string // required: pre-built image with Nix + sshd
	CPURequest      string // default: "2"
	MemoryRequest   string // default: "4Gi"
	CPULimit        string // default: "4"
	MemoryLimit     string // default: "8Gi"
	ImagePullSecret string // optional
}

// Provisioner creates and destroys builder Pods on Kubernetes.
type Provisioner struct {
	mu          sync.Mutex
	client      *kubernetes.Clientset
	cfg         Config
	builderArch map[string]string // builderID -> arch
}

// New creates a Kubernetes provisioner using in-cluster config.
func New(cfg Config) (*Provisioner, error) {
	if cfg.BuilderImage == "" {
		return nil, fmt.Errorf("BuilderImage is required")
	}
	if cfg.Namespace == "" {
		cfg.Namespace = "nix-builders"
	}
	if cfg.CPURequest == "" {
		cfg.CPURequest = "2"
	}
	if cfg.MemoryRequest == "" {
		cfg.MemoryRequest = "4Gi"
	}
	if cfg.CPULimit == "" {
		cfg.CPULimit = "4"
	}
	if cfg.MemoryLimit == "" {
		cfg.MemoryLimit = "8Gi"
	}

	restCfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("getting in-cluster config: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("creating k8s client: %w", err)
	}

	return &Provisioner{
		client:      clientset,
		cfg:         cfg,
		builderArch: make(map[string]string),
	}, nil
}

func (p *Provisioner) Name() string { return "kubernetes" }

// Create provisions a builder Pod for the given arch and waits until SSH is available.
func (p *Provisioner) Create(ctx context.Context, id string, config provisioner.Config, arch string) (string, error) {
	k8sArch, err := archToK8sArch(arch)
	if err != nil {
		return "", err
	}

	p.mu.Lock()
	p.builderArch[id] = arch
	p.mu.Unlock()

	name := resourceName(id)

	secret := buildSecret(name, p.cfg.Namespace, id, config)
	if _, err := p.client.CoreV1().Secrets(p.cfg.Namespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
		return "", fmt.Errorf("creating secret for builder %s: %w", id, err)
	}

	pod := p.buildPod(name, k8sArch)
	if _, err := p.client.CoreV1().Pods(p.cfg.Namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		_ = p.deleteSecret(context.Background(), name)
		return "", fmt.Errorf("creating pod for builder %s: %w", id, err)
	}

	ip, err := p.waitForBuilder(ctx, id, name)
	if err != nil {
		_ = p.deletePod(context.Background(), name)
		_ = p.deleteSecret(context.Background(), name)
		p.mu.Lock()
		delete(p.builderArch, id)
		p.mu.Unlock()
		return "", fmt.Errorf("waiting for builder %s: %w", id, err)
	}

	log.Printf("Builder %s: pod %s ready at %s", id, name, ip)
	return ip, nil
}

// Destroy deletes the builder Pod and its Secret.
func (p *Provisioner) Destroy(ctx context.Context, id string) error {
	p.mu.Lock()
	delete(p.builderArch, id)
	p.mu.Unlock()

	name := resourceName(id)
	var errs []string

	if err := p.deletePod(ctx, name); err != nil {
		errs = append(errs, fmt.Sprintf("delete pod: %v", err))
	}
	if err := p.deleteSecret(ctx, name); err != nil {
		errs = append(errs, fmt.Sprintf("delete secret: %v", err))
	}

	if len(errs) > 0 {
		return fmt.Errorf("destroy builder %s: %s", id, strings.Join(errs, "; "))
	}
	log.Printf("Builder %s: pod and secret deleted", id)
	return nil
}

func (p *Provisioner) deletePod(ctx context.Context, name string) error {
	foreground := metav1.DeletePropagationForeground
	err := p.client.CoreV1().Pods(p.cfg.Namespace).Delete(ctx, name, metav1.DeleteOptions{
		PropagationPolicy: &foreground,
	})
	if err != nil && !k8serrors.IsNotFound(err) {
		return err
	}
	return nil
}

func (p *Provisioner) deleteSecret(ctx context.Context, name string) error {
	err := p.client.CoreV1().Secrets(p.cfg.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return err
	}
	return nil
}

func (p *Provisioner) waitForBuilder(ctx context.Context, id, podName string) (string, error) {
	log.Printf("Builder %s: waiting for pod to become Running", id)
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		pod, err := p.client.CoreV1().Pods(p.cfg.Namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			return "", fmt.Errorf("getting pod: %w", err)
		}

		switch pod.Status.Phase {
		case corev1.PodFailed:
			return "", fmt.Errorf("pod %s entered Failed state", podName)
		case corev1.PodRunning:
			if pod.Status.PodIP != "" {
				ip := pod.Status.PodIP
				log.Printf("Builder %s: pod running at %s, waiting for SSH", id, ip)
				if err := waitForSSH(ctx, id, ip); err != nil {
					return "", err
				}
				return ip, nil
			}
		}

		time.Sleep(1 * time.Second)
	}
}

func waitForSSH(ctx context.Context, id, ip string) error {
	addr := ip + ":22"
	for i := 0; i < 60; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err == nil {
			conn.Close()
			log.Printf("Builder %s: SSH ready at %s", id, addr)
			return nil
		}
		time.Sleep(1 * time.Second)
	}
	return fmt.Errorf("SSH did not become available at %s within 60s", addr)
}

func buildSecret(name, namespace, id string, config provisioner.Config) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    builderLabels(id),
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"authorized_keys":  config.ProxyPublicKey,
			"store_key":        config.BuilderStorePrivKey,
			"store_host":       []byte(config.StoreHost),
			"store_host_port":  []byte(fmt.Sprintf("%d", config.StoreHostSSHPort)),
			"store_host_user":  []byte(config.StoreHostUser),
			"store_host_key":   []byte(config.StoreHostKey),
		},
	}
}

func (p *Provisioner) buildPod(name, k8sArch string) *corev1.Pod {
	defaultMode := int32(0400)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: p.cfg.Namespace,
			Labels:    builderLabels(name),
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			NodeSelector: map[string]string{
				"kubernetes.io/arch": k8sArch,
			},
			Volumes: []corev1.Volume{
				{
					Name: "builder-secrets",
					VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{
							SecretName:  name,
							DefaultMode: &defaultMode,
						},
					},
				},
			},
			Containers: []corev1.Container{
				{
					Name:  "builder",
					Image: p.cfg.BuilderImage,
					Ports: []corev1.ContainerPort{
						{ContainerPort: 22, Protocol: corev1.ProtocolTCP},
					},
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse(p.cfg.CPURequest),
							corev1.ResourceMemory: resource.MustParse(p.cfg.MemoryRequest),
						},
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse(p.cfg.CPULimit),
							corev1.ResourceMemory: resource.MustParse(p.cfg.MemoryLimit),
						},
					},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "builder-secrets",
							MountPath: "/secrets",
							ReadOnly:  true,
						},
					},
				},
			},
		},
	}

	if p.cfg.ImagePullSecret != "" {
		pod.Spec.ImagePullSecrets = []corev1.LocalObjectReference{
			{Name: p.cfg.ImagePullSecret},
		}
	}

	return pod
}

func archToK8sArch(arch string) (string, error) {
	switch arch {
	case "aarch64":
		return "arm64", nil
	case "x86_64":
		return "amd64", nil
	default:
		return "", fmt.Errorf("unsupported arch for kubernetes provisioner: %s", arch)
	}
}

func resourceName(id string) string {
	return "nix-builder-" + id
}

func builderLabels(id string) map[string]string {
	return map[string]string{
		"app":        "nix-builder",
		"builder-id": id,
	}
}
