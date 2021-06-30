package test

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/services/keyvault/auth"
	"github.com/Azure/azure-sdk-for-go/services/keyvault/v7.0/keyvault"
	"github.com/Azure/go-autorest/autorest/azure"
	"github.com/gruntwork-io/terratest/modules/terraform"
	"github.com/stretchr/testify/require"
	apimeta "k8s.io/apimachinery/pkg/api/meta"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kustomizev1 "github.com/fluxcd/kustomize-controller/api/v1beta1"
	"github.com/fluxcd/pkg/apis/meta"
	sourcev1 "github.com/fluxcd/source-controller/api/v1beta1"
)

const fluxBin = "../../bin/flux"
const sharedKeyVault = "azure-e2e-shared"
const gitUrl = "ssh://git@ssh.dev.azure.com/v3/flux-azure/e2e/fleet-infra"

func TestAzureE2E(t *testing.T) {
	ctx := context.TODO()
	tmpDir, err := ioutil.TempDir("", "*-azure-e2e")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	t.Log("Running Terraform init and apply")
	terraformOpts := terraform.WithDefaultRetryableErrors(t, &terraform.Options{
		TerraformDir: "./terraform",
	})
	deferDestroy := false
	if deferDestroy {
		defer terraform.Destroy(t, terraformOpts)
	}
	//terraform.InitAndApply(t, terraformOpts)
	kubeconfig := terraform.Output(t, terraformOpts, "aks_kube_config")
	aksHost := terraform.Output(t, terraformOpts, "aks_host")
	aksCert := terraform.Output(t, terraformOpts, "aks_client_certificate")
	aksKey := terraform.Output(t, terraformOpts, "aks_client_key")
	aksCa := terraform.Output(t, terraformOpts, "aks_cluster_ca_certificate")
	eventHubConnectionString := terraform.Output(t, terraformOpts, "event_hub_connection_string")

	t.Log("Installing Flux")
	kubeconfigPath, kubeClient, err := getKubernetesCredentials(tmpDir, kubeconfig, aksHost, aksCert, aksKey, aksCa)
	require.NoError(t, err)
	err = installFlux(ctx, kubeconfigPath)
	require.NoError(t, err)
	err = bootrapFlux(ctx, tmpDir, kubeconfigPath)
	require.NoError(t, err)

	t.Log("Verifying Flux installation")
	require.Eventually(t, func() bool {
		err := verifyGitAndKustomization(ctx, kubeClient, "flux-system", "flux-system")
		if err != nil {
			return false
		}
		return true
	}, 5*time.Second, 1*time.Second)

	t.Log("Verifying application-gitops namespaces")
	var applicationNsTest = []struct {
		name   string
		scheme string
		ref    string
	}{
		{
			name:   "https from 'main' branch",
			scheme: "https",
			ref:    "main",
		},
		{
			name:   "https from 'feature/branch' branch",
			scheme: "https",
			ref:    "feature-branch",
		},
		{
			name:   "https from 'v1' tag",
			scheme: "https",
			ref:    "v1-tag",
		},
	}
	for _, tt := range applicationNsTest {
		t.Run(tt.name, func(t *testing.T) {
			require.Eventually(t, func() bool {
				name := fmt.Sprintf("application-gitops-%s-%s", tt.scheme, tt.ref)
				err := verifyGitAndKustomization(ctx, kubeClient, "flux-system", name)
				if err != nil {
					return false
				}
				return true
			}, 5*time.Second, 1*time.Second)

			t.Log("Check commit status notifications")
			// TODO

			t.Log("Check azure event hub events")

		})
	}
}

// getKubernetesCredentials returns a path to a kubeconfig file and a kube client instance.
func getKubernetesCredentials(tmpDir, kubeconfig, aksHost, aksCert, aksKey, aksCa string) (string, client.Client, error) {
	kubeconfigPath := fmt.Sprintf("%s/kubeconfig", tmpDir)
	os.WriteFile(kubeconfigPath, []byte(kubeconfig), 0750)
	kubeCfg := &rest.Config{
		Host: aksHost,
		TLSClientConfig: rest.TLSClientConfig{
			CertData: []byte(aksCert),
			KeyData:  []byte(aksKey),
			CAData:   []byte(aksCa),
		},
	}
	err := sourcev1.AddToScheme(scheme.Scheme)
	if err != nil {
		return "", nil, err
	}
	err = kustomizev1.AddToScheme(scheme.Scheme)
	if err != nil {
		return "", nil, err
	}
	kubeClient, err := client.New(kubeCfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		return "", nil, err
	}
	return kubeconfigPath, kubeClient, nil
}

// installFlux adds the core Flux components to the cluster specified in the kubeconfig file.
func installFlux(ctx context.Context, kubeconfigPath string) error {
	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(timeoutCtx, fluxBin, "install", "--components-extra", "image-reflector-controller,image-automation-controller", "--kubeconfig", kubeconfigPath)
	_, err := cmd.Output()
	if err != nil {
		return err
	}
	return nil
}

// bootrapFlux adds gitrespository and kustomization resources to sync from a repository
func bootrapFlux(ctx context.Context, tmpDir, kubeconfigPath string) error {
	// Get ssh private key
	authorizer, err := auth.NewAuthorizerFromCLI()
	if err != nil {
		return err
	}
	vaultClient := keyvault.New()
	vaultClient.Authorizer = authorizer
	vaultBaseURL := fmt.Sprintf("https://%s.%s", sharedKeyVault, azure.PublicCloud.KeyVaultDNSSuffix)
	patResult, err := vaultClient.GetSecret(ctx, vaultBaseURL, "azdo-pat", "")
	if err != nil {
		return err
	}
	idRsaResult, err := vaultClient.GetSecret(ctx, vaultBaseURL, "id-rsa", "")
	if err != nil {
		return err
	}
	idRsaPath := filepath.Join(tmpDir, "id_rsa")
	err = os.WriteFile(idRsaPath, []byte(*idRsaResult.Value), 0600)
	if err != nil {
		return err
	}

	// Create flux-system git repository
	gitTimeoutCtx, gitCancel := context.WithTimeout(ctx, 5*time.Minute)
	defer gitCancel()
	cmd := exec.CommandContext(gitTimeoutCtx, fluxBin, "create", "source", "git", "flux-system", "--no-prompt", "--url", gitUrl, "--branch", "main", "--private-key-file", idRsaPath, "--git-implementation", "libgit2", "--kubeconfig", kubeconfigPath)
	_, err = cmd.Output()
	if err != nil {
		return err
	}

	// Create flux-system kustomization
	kustomizeTimeoutCtx, kustomizeCancel := context.WithTimeout(ctx, 5*time.Minute)
	defer kustomizeCancel()
	cmd = exec.CommandContext(kustomizeTimeoutCtx, fluxBin, "create", "kustomization", "flux-system", "--source", "flux-system", "--path", "./clusters/prod", "--prune", "true", "--interval", "1m", "--kubeconfig", kubeconfigPath)
	_, err = cmd.Output()
	if err != nil {
		return err
	}

	// Add https credentials
	httpsTimeoutCtx, httpsCancel := context.WithTimeout(ctx, 5*time.Minute)
	defer httpsCancel()
	cmd = exec.CommandContext(httpsTimeoutCtx, fluxBin, "create", "secret", "git", "https-credentials", "--url", "https://example.com", "--username", "git", "--password", *patResult.Value, "--kubeconfig", kubeconfigPath)
	_, err = cmd.Output()
	if err != nil {
		return err
	}

	// Add azure devops pat
	patTimeoutCtx, patCancel := context.WithTimeout(ctx, 5*time.Minute)
	defer patCancel()
	cmd = exec.CommandContext(patTimeoutCtx, "kubectl", "--namespace", "flux-system", "create", "secret", "generic", "azdo-pat", "--from-literal", fmt.Sprintf("token=%s", *patResult.Value), "--kubeconfig", kubeconfigPath)
	_, err = cmd.Output()
	if err != nil {
		return err
	}

	return nil
}

// verifyGitAndKustomization checks that the gitrespository and kustomization combination are working properly.
func verifyGitAndKustomization(ctx context.Context, kubeClient client.Client, namespace, name string) error {
	nn := types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}
	source := &sourcev1.GitRepository{}
	err := kubeClient.Get(ctx, nn, source)
	if err != nil {
		return err
	}
	if apimeta.IsStatusConditionFalse(source.Status.Conditions, meta.ReadyCondition) {
		return fmt.Errorf("source not ready")
	}
	kustomization := &kustomizev1.Kustomization{}
	err = kubeClient.Get(ctx, nn, kustomization)
	if err != nil {
		return err
	}
	if apimeta.IsStatusConditionFalse(kustomization.Status.Conditions, meta.ReadyCondition) {
		return fmt.Errorf("kustomization not ready")
	}
	return nil
}
