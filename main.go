package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	memory "k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"

	"sigs.k8s.io/yaml"
)

// template describes a policy template object we need to fetch.
type template struct {
	GVK  schema.GroupVersionKind
	Name string
}

func main() {
	flag.Parse()
	if flag.NArg() < 1 {
		log.Fatalf("usage: %s <policyname>", os.Args[0])
	}
	policyName := flag.Arg(0)

	cfg, err := rest.InClusterConfig()
	if err != nil {
		kubeconfig := clientcmd.RecommendedHomeFile
		if kc := os.Getenv("KUBECONFIG"); kc != "" {
			kubeconfig = kc
		}
		cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			log.Fatalf("build kubeconfig: %v", err)
		}
	}

	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		log.Fatalf("create dynamic client: %v", err)
	}

	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		log.Fatalf("create discovery client: %v", err)
	}
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(dc))

	ctx := context.Background()

	policyGVR := schema.GroupVersionResource{
		Group:    "policy.open-cluster-management.io",
		Version:  "v1",
		Resource: "policies",
	}

	policy, err := dyn.Resource(policyGVR).Namespace("acm-policies").Get(ctx, policyName, metav1.GetOptions{})
	if err != nil {
		log.Fatalf("get policy: %v", err)
	}

	clusters, err := extractClusterNames(policy)
	if err != nil {
		log.Fatalf("extract clusters: %v", err)
	}

	templates, err := extractTemplates(policy)
	if err != nil {
		log.Fatalf("extract templates: %v", err)
	}

	var out []map[string]interface{}
	for _, cluster := range clusters {
		for _, tmpl := range templates {
			info, err := fetchResource(ctx, dyn, mapper, cluster, tmpl)
			if err != nil {
				log.Printf("warn: %v", err)
				continue
			}
			out = append(out, info)
		}
	}

	data, err := yaml.Marshal(out)
	if err != nil {
		log.Fatalf("marshal yaml: %v", err)
	}
	fmt.Print(string(data))
}

func extractClusterNames(policy *unstructured.Unstructured) ([]string, error) {
	status, ok := policy.Object["status"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("policy missing status")
	}
	arr, ok := status["status"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("policy status missing status array")
	}
	var clusters []string
	for _, item := range arr {
		if m, ok := item.(map[string]interface{}); ok {
			if cn, ok := m["clusternamespace"].(string); ok {
				clusters = append(clusters, cn)
			}
		}
	}
	return clusters, nil
}

func extractTemplates(policy *unstructured.Unstructured) ([]template, error) {
	spec, ok := policy.Object["spec"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("policy missing spec")
	}
	pts, ok := spec["policy-templates"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("policy spec missing policy-templates")
	}
	var res []template
	for _, pt := range pts {
		ptMap, ok := pt.(map[string]interface{})
		if !ok {
			continue
		}
		od, ok := ptMap["objectDefinition"].(map[string]interface{})
		if !ok {
			continue
		}
		apiVersion, _ := od["apiVersion"].(string)
		kind, _ := od["kind"].(string)
		metadata, _ := od["metadata"].(map[string]interface{})
		name, _ := metadata["name"].(string)
		gvk := schema.FromAPIVersionAndKind(apiVersion, kind)
		res = append(res, template{GVK: gvk, Name: name})
	}
	return res, nil
}

func fetchResource(ctx context.Context, dyn dynamic.Interface, mapper meta.RESTMapper, cluster string, tmpl template) (map[string]interface{}, error) {
	mapping, err := mapper.RESTMapping(tmpl.GVK.GroupKind(), tmpl.GVK.Version)
	if err != nil {
		return nil, fmt.Errorf("mapping for %s: %w", tmpl.GVK.String(), err)
	}
	// Build resource identifier string like kind.version.group
	resourceStr := strings.ToLower(tmpl.GVK.Kind) + "." + tmpl.GVK.Version + "." + tmpl.GVK.Group

	if cluster == "local-cluster" {
		obj, err := dyn.Resource(mapping.Resource).Namespace(cluster).Get(ctx, tmpl.Name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("get local resource %s: %w", tmpl.Name, err)
		}
		return extractInfo(obj), nil
	}

	uid, err := randomHex(20)
	if err != nil {
		return nil, fmt.Errorf("generate uid: %w", err)
	}
	mcvGVR := schema.GroupVersionResource{
		Group:    "view.open-cluster-management.io",
		Version:  "v1beta1",
		Resource: "managedclusterviews",
	}
	mcv := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "view.open-cluster-management.io/v1beta1",
			"kind":       "ManagedClusterView",
			"metadata": map[string]interface{}{
				"name":      uid,
				"namespace": cluster,
				"labels": map[string]interface{}{
					"viewName": uid,
				},
			},
			"spec": map[string]interface{}{
				"scope": map[string]interface{}{
					"name":      tmpl.Name,
					"resource":  resourceStr,
					"namespace": cluster,
				},
			},
		},
	}
	_, err = dyn.Resource(mcvGVR).Namespace(cluster).Create(ctx, mcv, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create managedclusterview: %w", err)
	}
	defer func() {
		_ = dyn.Resource(mcvGVR).Namespace(cluster).Delete(context.Background(), uid, metav1.DeleteOptions{})
	}()

	// Wait for result to appear
	var result map[string]interface{}
	for i := 0; i < 30; i++ {
		current, err := dyn.Resource(mcvGVR).Namespace(cluster).Get(ctx, uid, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("get managedclusterview: %w", err)
		}
		if status, ok := current.Object["status"].(map[string]interface{}); ok {
			if res, ok := status["result"].(map[string]interface{}); ok {
				result = res
				break
			}
		}
		time.Sleep(1 * time.Second)
	}
	if result == nil {
		return nil, fmt.Errorf("timed out waiting for managedclusterview result")
	}
	obj := &unstructured.Unstructured{Object: result}
	return extractInfo(obj), nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func extractInfo(obj *unstructured.Unstructured) map[string]interface{} {
	out := map[string]interface{}{
		"apiVersion": obj.GetAPIVersion(),
		"kind":       obj.GetKind(),
		"metadata": map[string]interface{}{
			"name":      obj.GetName(),
			"namespace": obj.GetNamespace(),
		},
	}
	status, _ := obj.Object["status"].(map[string]interface{})
	if status != nil {
		if c, ok := status["compliant"]; ok {
			out["compliant"] = c
		}
		// condition from compliancyDetails[].conditions
		var condition interface{}
		if details, ok := status["compliancyDetails"].([]interface{}); ok {
			for _, d := range details {
				if dm, ok := d.(map[string]interface{}); ok {
					if conds, ok := dm["conditions"]; ok {
						condition = conds
						break
					}
				}
			}
		}
		if condition == nil {
			if conds, ok := status["conditions"].([]interface{}); ok {
				for _, c := range conds {
					if cm, ok := c.(map[string]interface{}); ok {
						if t, ok := cm["type"].(string); ok && t == "Compliant" {
							condition = cm
							break
						}
					}
				}
			}
		}
		if condition != nil {
			out["condition"] = condition
		}
	}
	return out
}
