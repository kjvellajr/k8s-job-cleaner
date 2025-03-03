/*
Copyright 2025.

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

package controller

import (
	"context"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

// JobReconciler reconciles a Job object
type JobReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

const (
	appOwnerIndexKey = ".metadata.controller"
)

// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=batch,resources=jobs/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Job object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.2/pkg/reconcile
func (r *JobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	obj := &batchv1.Job{}
	if err := r.Get(ctx, req.NamespacedName, obj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	gvk, err := apiutil.GVKForObject(&batchv1.CronJob{}, r.Scheme)
	if err != nil {
		return ctrl.Result{}, err
	}

	// make sure Job is owned by a CronJob
	ownerReference := metav1.GetControllerOf(obj)
	if ownerReference == nil {
		return ctrl.Result{}, nil
	}
	if ownerReference.APIVersion != gvk.GroupVersion().String() || ownerReference.Kind != gvk.Kind {
		return ctrl.Result{}, nil
	}

	// find all related Jobs related to this one (has the same CronJob parent)
	jobList := batchv1.JobList{}
	if err := r.List(ctx, &jobList, client.MatchingFields{appOwnerIndexKey: ownerReference.Name}); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// determine which sibling Jobs should be cleaned
	siblingsToDelete := []batchv1.Job{}
	for _, sib := range jobList.Items {
		if sib.Name == obj.Name {
			continue // ignore current Job
		}
		if sib.Status.StartTime == nil || obj.Status.StartTime == nil {
			continue // ignore siblings that have not started
		}
		if sib.Status.StartTime.Time.After(obj.Status.StartTime.Time) {
			continue // ignore siblings that started after this Job
		}
		if obj.Status.CompletionTime == nil {
			continue // do not delete sibilings for Jobs that are not complete
		}
		if sib.Status.Failed > 0 {
			siblingsToDelete = append(siblingsToDelete, sib)
		}
	}

	// delete Pods owned by siblingsToDelete
	for _, sibling := range siblingsToDelete {
		podList := &corev1.PodList{}
		err := r.List(ctx, podList, client.InNamespace(req.Namespace), client.MatchingLabels(sibling.Spec.Template.Labels))
		if err != nil {
			return ctrl.Result{}, err
		}
		for _, pod := range podList.Items {
			// todo: check to see if Pod is actually failed before deleting
			log.Info("deleting sibling pod", "cronJob", ownerReference.Name, "job", sibling.Name, "pod", pod.Name)
			if err := r.Delete(ctx, &pod); client.IgnoreNotFound(err) != nil {
				return ctrl.Result{}, err
			}
		}
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *JobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := r.setupIndexer(mgr); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&batchv1.Job{}).
		Named("job").
		Complete(r)
}

func (r *JobReconciler) setupIndexer(mgr ctrl.Manager) error {
	gvk, err := apiutil.GVKForObject(&batchv1.CronJob{}, r.Scheme)
	if err != nil {
		return nil
	}

	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &batchv1.Job{}, appOwnerIndexKey, func(rawObj client.Object) []string {
		// grab the object, extract the owner...
		obj := rawObj.(*batchv1.Job)
		owner := metav1.GetControllerOf(obj)
		if owner == nil {
			return nil
		}

		// ...make sure it's a CronJob...
		if owner.APIVersion != gvk.GroupVersion().String() || owner.Kind != gvk.Kind {
			return nil
		}

		// ...and if so, return it
		return []string{owner.Name}
	}); err != nil {
		return err
	}

	return nil
}
