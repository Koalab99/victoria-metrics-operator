package vmalert

import (
	"context"
	"fmt"
	"hash/fnv"
	"sort"
	"strconv"
	"strings"

	vmv1beta1 "github.com/VictoriaMetrics/operator/api/operator/v1beta1"
	"github.com/VictoriaMetrics/operator/internal/controller/operator/factory/finalize"
	"github.com/VictoriaMetrics/operator/internal/controller/operator/factory/k8stools"
	"github.com/VictoriaMetrics/operator/internal/controller/operator/factory/logger"
	"github.com/VictoriaMetrics/operator/internal/controller/operator/factory/reconcile"
	"github.com/ghodss/yaml"
	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var badConfigsTotal = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "operator_vmalert_bad_objects_count",
	Help: "Number of incorrect objects by controller",
	ConstLabels: prometheus.Labels{
		"controller": "vmrules",
	},
})

func init() {
	metrics.Registry.MustRegister(badConfigsTotal)
}

var (
	managedByOperatorLabel      = "managed-by"
	managedByOperatorLabelValue = "vm-operator"
	managedByOperatorLabels     = map[string]string{
		managedByOperatorLabel: managedByOperatorLabelValue,
	}
)

var defAlert = `
groups:
- name: vmAlertGroup
  rules:
     - alert: error writing to remote
       for: 1m
       expr: rate(vmalert_remotewrite_errors_total[1m]) > 0
       labels:
         host: "{{ $labels.instance }}"
       annotations:
         summary: " error writing to remote writer from vmaler{{ $value|humanize }}"
         description: "error writing to remote writer from vmaler {{$labels}}"
         back: "error rate is ok at vmalert "
`

// CreateOrUpdateRuleConfigMaps conditionally selects vmrules and stores content at configmaps
func CreateOrUpdateRuleConfigMaps(ctx context.Context, cr *vmv1beta1.VMAlert, rclient client.Client) ([]string, error) {
	// fast path
	if cr.IsUnmanaged() {
		return nil, nil
	}
	newRules, err := selectRulesUpdateStatus(ctx, cr, rclient)
	if err != nil {
		return nil, err
	}

	newConfigMaps := makeRulesConfigMaps(cr, newRules)
	currentCMs := make([]corev1.ConfigMap, len(newConfigMaps))
	for idx, cm := range newConfigMaps {
		var existCM corev1.ConfigMap
		if err := rclient.Get(ctx, types.NamespacedName{Namespace: cm.Namespace, Name: cm.Name}, &existCM); err != nil {
			if errors.IsNotFound(err) {
				continue
			}
			return nil, err
		}
		currentCMs[idx] = existCM
	}

	newConfigMapNames := make([]string, 0, len(newConfigMaps))
	for _, cm := range newConfigMaps {
		newConfigMapNames = append(newConfigMapNames, cm.Name)
	}

	if len(currentCMs) == 0 {
		for _, cm := range newConfigMaps {
			logger.WithContext(ctx).Info(fmt.Sprintf("creating new ConfigMap %s for rules", cm.Name))
			err := rclient.Create(ctx, &cm)
			if err != nil {
				if errors.IsAlreadyExists(err) {
					continue
				}
				return nil, fmt.Errorf("failed to create Configmap: %s, err: %w", cm.Name, err)
			}
		}
		return newConfigMapNames, nil
	}

	// sort
	sort.Strings(newConfigMapNames)
	sort.Slice(currentCMs, func(i, j int) bool {
		return currentCMs[i].Name < currentCMs[j].Name
	})
	sort.Slice(newConfigMaps, func(i, j int) bool {
		return newConfigMaps[i].Name < newConfigMaps[j].Name
	})

	// compute diff for current and needed rules configmaps.
	toCreate, toUpdate := rulesCMDiff(currentCMs, newConfigMaps)
	for _, cm := range toCreate {
		logger.WithContext(ctx).Info(fmt.Sprintf("creating additional configmap=%s for rules", cm.Name))
		err = rclient.Create(ctx, &cm)
		if err != nil {
			if errors.IsAlreadyExists(err) {
				continue
			}
			return nil, fmt.Errorf("failed to create new rules Configmap: %s, err: %w", cm.Name, err)
		}
	}
	for _, cm := range toUpdate {
		if err := finalize.FreeIfNeeded(ctx, rclient, &cm); err != nil {
			return nil, err
		}
		logger.WithContext(ctx).Info(fmt.Sprintf("updating ConfigMap %s configuration", cm.Name))
		err = rclient.Update(ctx, &cm)
		if err != nil {
			return nil, fmt.Errorf("failed to update rules Configmap: %s, err: %w", cm.Name, err)
		}
	}

	if len(toCreate) > 0 || len(toUpdate) > 0 {
		// trigger sync for configmap
		logger.WithContext(ctx).Info("triggering pod config reload by changing annotation")
		err = k8stools.UpdatePodAnnotations(ctx, rclient, cr.PodLabels(), cr.Namespace)
		if err != nil {
			logger.WithContext(ctx).Error(err, "failed to update vmalert pod cm-sync annotation")
		}
	}

	return newConfigMapNames, nil
}

// rulesCMDiff - calculates diff between existing at k8s (current) configmaps with rules
// and generated by operator (new) configmaps.
// Configmaps are grouped by operations, that must be performed over them.
func rulesCMDiff(currentCMs []corev1.ConfigMap, newCMs []corev1.ConfigMap) (toCreate []corev1.ConfigMap, toUpdate []corev1.ConfigMap) {
	if len(newCMs) == 0 {
		return
	}
	if len(currentCMs) == 0 {
		return newCMs, nil
	}
	// calculate maps for update
	for _, newCM := range newCMs {
		var found bool
		for _, currentCM := range currentCMs {
			if newCM.Name == currentCM.Name {
				found = true
				newCM.Annotations = labels.Merge(currentCM.Annotations, newCM.Annotations)
				vmv1beta1.AddFinalizer(&newCM, &currentCM)
				if equality.Semantic.DeepEqual(newCM.Data, currentCM.Data) &&
					equality.Semantic.DeepEqual(newCM.Labels, currentCM.Labels) &&
					equality.Semantic.DeepEqual(newCM.Annotations, currentCM.Annotations) {
					break
				}
				toUpdate = append(toUpdate, newCM)
				break
			}
		}
		if !found {
			toCreate = append(toCreate, newCM)
		}
	}
	return toCreate, toUpdate
}

func selectRulesUpdateStatus(ctx context.Context, cr *vmv1beta1.VMAlert, rclient client.Client) (map[string]string, error) {
	var vmRules []*vmv1beta1.VMRule
	var namespacedNames []string
	if err := k8stools.VisitObjectsForSelectorsAtNs(ctx, rclient, cr.Spec.RuleNamespaceSelector, cr.Spec.RuleSelector, cr.Namespace, cr.Spec.SelectAllByDefault,
		func(list *vmv1beta1.VMRuleList) {
			for _, item := range list.Items {
				if !item.DeletionTimestamp.IsZero() {
					continue
				}
				vmRules = append(vmRules, item.DeepCopy())
				namespacedNames = append(namespacedNames, fmt.Sprintf("%s/%s", item.Namespace, item.Name))
			}
		}); err != nil {
		return nil, err
	}

	rules := make(map[string]string, len(vmRules))

	if cr.NeedDedupRules() {
		logger.WithContext(ctx).Info("deduplicating vmalert rules")
		vmRules = deduplicateRules(ctx, vmRules)
	}
	var badRules []*vmv1beta1.VMRule
	var cnt int
	for _, pRule := range vmRules {
		if err := pRule.Validate(); err != nil {
			pRule.Status.CurrentSyncError = err.Error()
			badRules = append(badRules, pRule)
			continue
		}
		content, err := generateContent(pRule.Spec, cr.Spec.EnforcedNamespaceLabel, pRule.Namespace)
		if err != nil {
			pRule.Status.CurrentSyncError = fmt.Sprintf("cannot generate content for rule: %s, err :%s", pRule.Name, err)
			badRules = append(badRules, pRule)
			continue
		}
		vmRules[cnt] = pRule
		cnt++
		rules[fmt.Sprintf("%s-%s.yaml", pRule.Namespace, pRule.Name)] = content
	}
	vmRules = vmRules[:cnt]

	ruleNames := make([]string, 0, len(rules))
	for name := range rules {
		ruleNames = append(ruleNames, name)
	}

	if len(rules) == 0 {
		// inject default rule
		// it's needed to start vmalert.
		rules["default-vmalert.yaml"] = defAlert
	}
	badConfigsTotal.Add(float64(len(badRules)))

	parentObject := fmt.Sprintf("%s.%s.vmalert", cr.Name, cr.Namespace)
	if err := reconcile.StatusForChildObjects(ctx, rclient, parentObject, vmRules); err != nil {
		return nil, err
	}
	if err := reconcile.StatusForChildObjects(ctx, rclient, parentObject, badRules); err != nil {
		return nil, fmt.Errorf("cannot update bad rules statuses: %w", err)
	}

	if len(namespacedNames) > 0 {
		logger.WithContext(ctx).Info(fmt.Sprintf("selected Rules count=d, invalid rules count=%d, namespaced names %s",
			len(namespacedNames), len(badRules), strings.Join(namespacedNames, ",")))
	}

	return rules, nil
}

func generateContent(promRule vmv1beta1.VMRuleSpec, enforcedNsLabel, ns string) (string, error) {
	if enforcedNsLabel != "" {
		for gi, group := range promRule.Groups {
			for ri := range group.Rules {
				if len(promRule.Groups[gi].Rules[ri].Labels) == 0 {
					promRule.Groups[gi].Rules[ri].Labels = map[string]string{}
				}
				promRule.Groups[gi].Rules[ri].Labels[enforcedNsLabel] = ns
			}
		}
	}
	content, err := yaml.Marshal(promRule)
	if err != nil {
		return "", fmt.Errorf("cannot unmarshal context for cm rule generation: %w", err)
	}
	return string(content), nil
}

// makeRulesConfigMaps takes a VMAlert configuration and rule files and
// returns a list of Kubernetes ConfigMaps to be later on mounted
// If the total size of rule files exceeds the Kubernetes ConfigMap limit,
// they are split up via the simple first-fit [1] bin packing algorithm. In the
// future this can be replaced by a more sophisticated algorithm, but for now
// simplicity should be sufficient.
// [1] https://en.wikipedia.org/wiki/Bin_packing_problem#First-fit_algorithm
func makeRulesConfigMaps(cr *vmv1beta1.VMAlert, ruleFiles map[string]string) []corev1.ConfigMap {
	buckets := []map[string]string{
		{},
	}
	currBucketIndex := 0

	// To make bin packing algorithm deterministic, sort ruleFiles filenames and
	// iterate over filenames instead of ruleFiles map (not deterministic).
	fileNames := []string{}
	for n := range ruleFiles {
		fileNames = append(fileNames, n)
	}
	sort.Strings(fileNames)

	for _, filename := range fileNames {
		// If rule file doesn't fit into current bucket, create new bucket.
		if bucketSize(buckets[currBucketIndex])+len(ruleFiles[filename]) > vmv1beta1.MaxConfigMapDataSize {
			buckets = append(buckets, map[string]string{})
			currBucketIndex++
		}
		buckets[currBucketIndex][filename] = ruleFiles[filename]
	}

	ruleFileConfigMaps := make([]corev1.ConfigMap, 0, len(buckets))
	for i, bucket := range buckets {
		cm := makeRulesConfigMap(cr, bucket)
		cm.Name = cm.Name + "-" + strconv.Itoa(i)
		ruleFileConfigMaps = append(ruleFileConfigMaps, cm)
	}

	return ruleFileConfigMaps
}

func bucketSize(bucket map[string]string) int {
	totalSize := 0
	for _, v := range bucket {
		totalSize += len(v)
	}

	return totalSize
}

func makeRulesConfigMap(cr *vmv1beta1.VMAlert, ruleFiles map[string]string) corev1.ConfigMap {
	ruleLabels := map[string]string{"vmalert-name": cr.Name}
	for k, v := range managedByOperatorLabels {
		ruleLabels[k] = v
	}

	return corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:            ruleConfigMapName(cr.Name),
			Namespace:       cr.Namespace,
			Labels:          ruleLabels,
			OwnerReferences: cr.AsOwner(),
			Finalizers:      []string{vmv1beta1.FinalizerName},
		},
		Data: ruleFiles,
	}
}

func ruleConfigMapName(vmName string) string {
	return "vm-" + vmName + "-rulefiles"
}

// deduplicateRules - takes list of vmRules and modifies it
// by removing duplicates.
// possible duplicates:
// group name across single vmRule. group might include non-duplicate rules.
// rules in group, must include uniq combination of values.
func deduplicateRules(ctx context.Context, origin []*vmv1beta1.VMRule) []*vmv1beta1.VMRule {
	// deduplicate rules across groups.
	for _, vmRule := range origin {
		for i, grp := range vmRule.Spec.Groups {
			uniqRules := make(map[uint64]struct{})
			rules := make([]vmv1beta1.Rule, 0, len(grp.Rules))
			for _, rule := range grp.Rules {
				ruleID := calculateRuleID(rule)
				if _, ok := uniqRules[ruleID]; ok {
					logger.WithContext(ctx).Info(fmt.Sprintf("duplicate rule=%q found at group=%q for vmrule=%q", rule.Expr, grp.Name, vmRule.Name))
				} else {
					uniqRules[ruleID] = struct{}{}
					rules = append(rules, rule)
				}
			}
			grp.Rules = rules
			vmRule.Spec.Groups[i] = grp
		}
	}
	return origin
}

func calculateRuleID(r vmv1beta1.Rule) uint64 {
	h := fnv.New64a()
	h.Write([]byte(r.Expr)) //nolint:errcheck
	if r.Record != "" {
		h.Write([]byte("recording")) //nolint:errcheck
		h.Write([]byte(r.Record))    //nolint:errcheck
	} else {
		h.Write([]byte("alerting")) //nolint:errcheck
		h.Write([]byte(r.Alert))    //nolint:errcheck
	}
	kv := sortMap(r.Labels)
	for _, i := range kv {
		h.Write([]byte(i.key))   //nolint:errcheck
		h.Write([]byte(i.value)) //nolint:errcheck
		h.Write([]byte("\xff"))  //nolint:errcheck
	}
	return h.Sum64()
}

type item struct {
	key, value string
}

func sortMap(m map[string]string) []item {
	var kv []item
	for k, v := range m {
		kv = append(kv, item{key: k, value: v})
	}
	sort.Slice(kv, func(i, j int) bool {
		return kv[i].key < kv[j].key
	})
	return kv
}
