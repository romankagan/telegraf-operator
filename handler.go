/*
Copyright 2018 The Kubernetes Authors.
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

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	admv1 "k8s.io/api/admission/v1beta1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apiserver/pkg/storage/names"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// +kubebuilder:webhook:path=/mutate-v1-pod,mutating=true,failurePolicy=fail,groups="",resources=pods,verbs=create;update,versions=v1,name=mpod.kb.io

// podInjector inject telegraf Pods
type podInjector struct {
	client  client.Client
	decoder *admission.Decoder
	names.NameGenerator
	TelegrafClassesSecretName string
	TelegrafDefaultClass      string
	ControllerNamespace       string
}

// podInjector adds an annotation to every incoming pods.
func (a *podInjector) Handle(ctx context.Context, req admission.Request) admission.Response {
	handlerLog := setupLog.WithName("inject-handler")

	marshaled, err := json.Marshal(req)
	if err != nil {
		log.Fatal(err)
	}
	handlerLog.V(9).Info("request=" + string(marshaled))

	if req.Operation == admv1.Delete {
		secret := &corev1.Secret{
			TypeMeta: metav1.TypeMeta{
				Kind:       "Secret",
				APIVersion: "v1",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("%s-%s", telegrafSecretPrefix, req.Name),
				Namespace: req.Namespace,
			},
		}
		err := a.client.Delete(ctx, secret)
		if err != nil {
			handlerLog.Info("secret=" + secret.Name + "/" + secret.Namespace + " error:" + err.Error())
			return admission.Allowed("telegraf-injector coudn't delete secret")
		}
		return admission.Allowed("telegraf-injector doesn't block pod deletions")
	}

	pod := &corev1.Pod{}
	err = a.decoder.Decode(req, pod)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	if skip(pod) {
		return admission.Allowed("telegraf-injector has no power over this pod")
	}
	classData, err := a.getClassData(pod)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	name := pod.GetName()
	if name == "" {
		name = names.SimpleNameGenerator.GenerateName(pod.GetGenerateName())
		pod.SetName(name)
		handlerLog.Info("name: " + name + ",  pod_getname=" + pod.GetName())
	}
	secret, err := addSidecar(pod, pod.GetName(), req.Namespace, classData)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	if req.Operation == admv1.Create {
		err = a.client.Create(ctx, secret)
		if err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}
	}
	if req.Operation == admv1.Update {
		err = a.client.Update(ctx, secret)
		if err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}
	}

	marshaledPod, err := json.Marshal(pod)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}

	return admission.PatchResponseFromRaw(req.Object.Raw, marshaledPod)
}

// podInjector implements inject.Client.
// A client will be automatically injected.

// InjectClient injects the client.
func (a *podInjector) InjectClient(c client.Client) error {
	a.client = c
	return nil
}

// podInjector implements admission.DecoderInjector.
// A decoder will be automatically injected.

// InjectDecoder injects the decoder.
func (a *podInjector) InjectDecoder(d *admission.Decoder) error {
	a.decoder = d
	return nil
}

func (p *podInjector) getClassData(pod *corev1.Pod) (string, error) {
	className := p.TelegrafDefaultClass
	if extClass, ok := pod.Annotations[TelegrafClass]; ok {
		className = extClass
	}
	secret := &corev1.Secret{}
	err := p.client.Get(context.Background(), client.ObjectKey{
		Namespace: p.ControllerNamespace,
		Name:      p.TelegrafClassesSecretName,
	}, secret)
	if err != nil {
		return "", err
	}
	for key, val := range secret.Data {
		if key != className {
			continue
		}
		return string(val), nil
	}
	return "", fmt.Errorf("telegraf-default-class '%s' couldn't be found in secret %s in namespace %s", className, p.TelegrafClassesSecretName, p.ControllerNamespace)
}
