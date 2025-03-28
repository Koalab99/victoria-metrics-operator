/*


Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
// Code generated by client-gen-v0.32. DO NOT EDIT.

package fake

import (
	operatorv1beta1 "github.com/VictoriaMetrics/operator/api/client/versioned/typed/operator/v1beta1"
	v1beta1 "github.com/VictoriaMetrics/operator/api/operator/v1beta1"
	gentype "k8s.io/client-go/gentype"
)

// fakeVMAlertmanagerConfigs implements VMAlertmanagerConfigInterface
type fakeVMAlertmanagerConfigs struct {
	*gentype.FakeClientWithList[*v1beta1.VMAlertmanagerConfig, *v1beta1.VMAlertmanagerConfigList]
	Fake *FakeOperatorV1beta1
}

func newFakeVMAlertmanagerConfigs(fake *FakeOperatorV1beta1, namespace string) operatorv1beta1.VMAlertmanagerConfigInterface {
	return &fakeVMAlertmanagerConfigs{
		gentype.NewFakeClientWithList[*v1beta1.VMAlertmanagerConfig, *v1beta1.VMAlertmanagerConfigList](
			fake.Fake,
			namespace,
			v1beta1.SchemeGroupVersion.WithResource("vmalertmanagerconfigs"),
			v1beta1.SchemeGroupVersion.WithKind("VMAlertmanagerConfig"),
			func() *v1beta1.VMAlertmanagerConfig { return &v1beta1.VMAlertmanagerConfig{} },
			func() *v1beta1.VMAlertmanagerConfigList { return &v1beta1.VMAlertmanagerConfigList{} },
			func(dst, src *v1beta1.VMAlertmanagerConfigList) { dst.ListMeta = src.ListMeta },
			func(list *v1beta1.VMAlertmanagerConfigList) []*v1beta1.VMAlertmanagerConfig {
				return gentype.ToPointerSlice(list.Items)
			},
			func(list *v1beta1.VMAlertmanagerConfigList, items []*v1beta1.VMAlertmanagerConfig) {
				list.Items = gentype.FromPointerSlice(items)
			},
		),
		fake,
	}
}
