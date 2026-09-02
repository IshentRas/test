package k8sstate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type Client struct {
	cs        kubernetes.Interface
	namespace string
	name      string
}

func New(namespace, name string) (*Client, error) {
	cfg, err := restConfig()
	if err != nil {
		return nil, err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Client{cs: cs, namespace: namespace, name: name}, nil
}

func restConfig() (*rest.Config, error) {
	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}
	home, _ := os.UserHomeDir()
	return clientcmd.BuildConfigFromFlags("", home+"/.kube/config")
}

func (c *Client) PatchRelease(ctx context.Context, activeSHA string, tags map[string]string) error {
	tagsJSON, err := json.Marshal(tags)
	if err != nil {
		return err
	}
	patch := map[string]any{
		"data": map[string]string{
			"ACTIVE_COMMIT": activeSHA,
			"ACTIVE_TAGS":   string(tagsJSON),
		},
	}
	raw, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = c.cs.CoreV1().ConfigMaps(c.namespace).Patch(ctx, c.name, types.MergePatchType, raw, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("patch configmap %s/%s: %w", c.namespace, c.name, err)
	}
	return nil
}

func (c *Client) GetRelease(ctx context.Context) (active string, tagsJSON string, err error) {
	cm, err := c.cs.CoreV1().ConfigMaps(c.namespace).Get(ctx, c.name, metav1.GetOptions{})
	if err != nil {
		return "", "", err
	}
	if cm.Data == nil {
		return "", "{}", nil
	}
	return cm.Data["ACTIVE_COMMIT"], cm.Data["ACTIVE_TAGS"], nil
}

func Watch(ctx context.Context, namespace, name string, onChange func(active, tags string)) error {
	cfg, err := restConfig()
	if err != nil {
		return err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}
	w, err := cs.CoreV1().ConfigMaps(namespace).Watch(ctx, metav1.ListOptions{
		FieldSelector: "metadata.name=" + name,
	})
	if err != nil {
		return err
	}
	defer w.Stop()

	var lastActive, lastTags string
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-w.ResultChan():
			if !ok {
				return fmt.Errorf("configmap watch closed")
			}
			cm, ok := ev.Object.(*corev1.ConfigMap)
			if !ok {
				continue
			}
			active := ""
			tags := "{}"
			if cm.Data != nil {
				active = cm.Data["ACTIVE_COMMIT"]
				if cm.Data["ACTIVE_TAGS"] != "" {
					tags = cm.Data["ACTIVE_TAGS"]
				}
			}
			if active == lastActive && tags == lastTags {
				continue
			}
			lastActive, lastTags = active, tags
			onChange(active, tags)
		}
	}
}
