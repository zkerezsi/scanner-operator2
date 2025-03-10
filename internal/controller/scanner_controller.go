/*
Copyright 2024.

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
	"slices"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	scannerv1 "github.com/kerezsiz42/scanner-operator2/api/v1"
	"github.com/kerezsiz42/scanner-operator2/internal/service"
)

// ScannerReconciler reconciles a Scanner object
type ScannerReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	ScanService      service.ScanServiceInterface
	JobObjectService service.JobObjectServiceInterface
}

// +kubebuilder:rbac:groups=scanner.zoltankerezsi.xyz,resources=scanners,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=scanner.zoltankerezsi.xyz,resources=scanners/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=scanner.zoltankerezsi.xyz,resources=scanners/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods,verbs=list;watch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=list;watch;create

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Scanner object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.18.4/pkg/reconcile
func (r *ScannerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	reconcilerLog := log.FromContext(ctx)

	scanner := &scannerv1.Scanner{}
	if err := r.Get(ctx, req.NamespacedName, scanner); err != nil {
		reconcilerLog.Error(err, "unable to list scanner resources")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	scanResults, err := r.ScanService.ListScanResults()
	if err != nil {
		reconcilerLog.Error(err, "failed to list scan results")
		return ctrl.Result{}, r.nextStatusCondition(ctx, scanner, scannerv1.Failed)
	}

	scannedImageIDs := []string{}
	for _, scanResult := range scanResults {
		scannedImageIDs = append(scannedImageIDs, scanResult.ImageID)
	}

	labelRequirement, err := labels.NewRequirement(scanner.Spec.IgnoreLabel, selection.NotEquals, []string{"true"})
	if err != nil {
		reconcilerLog.Error(err, "failed to get IgnoreLabel requirement")
		return ctrl.Result{}, r.nextStatusCondition(ctx, scanner, scannerv1.Failed)
	}

	podList := &corev1.PodList{}
	if err := r.List(ctx, podList, &client.ListOptions{
		Namespace:     scanner.Namespace,
		LabelSelector: labels.NewSelector().Add(*labelRequirement),
	}); err != nil {
		reconcilerLog.Error(err, "failed to list pods")
		return ctrl.Result{}, r.nextStatusCondition(ctx, scanner, scannerv1.Failed)
	}

	imageID := ""
OuterLoop:
	for _, pod := range podList.Items {
		// TODO: Handle init containers as well
		for _, containerStatus := range pod.Status.ContainerStatuses {
			if !slices.Contains(scannedImageIDs, containerStatus.ImageID) {
				imageID = containerStatus.ImageID
				break OuterLoop
			}
		}
	}

	if imageID == "" {
		reconcilerLog.Info("all images scanned, successfully reconciled")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, r.nextStatusCondition(ctx, scanner, scannerv1.Reconciled)
	}

	jobList := &batchv1.JobList{}
	if err := r.List(ctx, jobList, &client.ListOptions{
		Namespace: scanner.Namespace,
	}); err != nil {
		reconcilerLog.Error(err, "failed to list jobs")
		return ctrl.Result{}, r.nextStatusCondition(ctx, scanner, scannerv1.Failed)
	}

	for _, job := range jobList.Items {
		if job.Status.Succeeded == 0 {
			reconcilerLog.Info("job is still in progress")
			return ctrl.Result{}, r.nextStatusCondition(ctx, scanner, scannerv1.Waiting)
		}
	}

	nextJob, err := r.JobObjectService.Create(imageID, scanner.Namespace)
	if err != nil {
		reconcilerLog.Error(err, "failed to create job from template")
		return ctrl.Result{}, r.nextStatusCondition(ctx, scanner, scannerv1.Failed)
	}

	if err := ctrl.SetControllerReference(scanner, nextJob, r.Scheme); err != nil {
		reconcilerLog.Error(err, "failed to set controller reference on job")
		return ctrl.Result{}, r.nextStatusCondition(ctx, scanner, scannerv1.Failed)
	}

	if err := r.Create(ctx, nextJob); err != nil {
		reconcilerLog.Error(err, "failed to create job")
		return ctrl.Result{}, r.nextStatusCondition(ctx, scanner, scannerv1.Failed)
	}

	reconcilerLog.Info("new job created")
	return ctrl.Result{}, r.nextStatusCondition(ctx, scanner, scannerv1.Scanning)
}

func (r *ScannerReconciler) mapPodsToRequests(ctx context.Context, pod client.Object) []reconcile.Request {
	scannerList := &scannerv1.ScannerList{}
	if err := r.List(ctx, scannerList, &client.ListOptions{Namespace: pod.GetNamespace()}); err != nil {
		return []reconcile.Request{}
	}

	if len(scannerList.Items) > 0 {
		return []reconcile.Request{{NamespacedName: types.NamespacedName{
			Name:      scannerList.Items[0].Name,
			Namespace: scannerList.Items[0].Namespace,
		}}}
	}

	return []reconcile.Request{}
}

func (r *ScannerReconciler) nextStatusCondition(
	ctx context.Context,
	scanner *scannerv1.Scanner,
	reason scannerv1.StatusReason,
) error {
	status := metav1.ConditionFalse
	if reason == scannerv1.Reconciled {
		status = metav1.ConditionTrue
	}

	changed := meta.SetStatusCondition(&scanner.Status.Conditions, metav1.Condition{
		Type:   "Ready",
		Status: status,
		Reason: string(reason),
	})

	if !changed {
		return nil
	}

	return r.Status().Update(ctx, scanner)
}

// SetupWithManager sets up the controller with the Manager.
func (r *ScannerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&scannerv1.Scanner{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Owns(&batchv1.Job{}).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(r.mapPodsToRequests)).
		Complete(r)
}
