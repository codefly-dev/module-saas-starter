package frontendpayload

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/containers/azcontainerregistry"
	"github.com/codefly-dev/core/solutionhost"
	"oras.land/oras-go/v2/registry"
)

// The same workload-reader stopgap as workload.go, using Azure's credential,
// token exchange and renewal implementation rather than another auth flow.
type azureWorkloadSource struct {
	client     *azcontainerregistry.BlobClient
	repository string
}

func newAzureWorkloadSource(repository string) (Source, error) {
	ref, err := registry.ParseReference(repository)
	if err != nil || ref.Reference != "" || ref.Repository == "" {
		return nil, errors.New("frontend artifact repository must name one repository without a tag or digest")
	}
	credential, err := azidentity.NewWorkloadIdentityCredential(nil)
	if err != nil {
		return nil, &readerIdentityError{"Azure frontend artifact reads require the pod's bound workload identity"}
	}
	return azureRegistrySource(ref, credential, http.DefaultTransport)
}

func azureRegistrySource(ref registry.Reference, credential azcore.TokenCredential, base http.RoundTripper) (Source, error) {
	client, err := azcontainerregistry.NewBlobClient("https://"+ref.Registry, credential,
		&azcontainerregistry.BlobClientOptions{ClientOptions: azcore.ClientOptions{
			Transport: &http.Client{Timeout: 30 * time.Second,
				Transport: repositoryTransport{host: ref.Registry, scheme: "https", base: base}},
		}})
	if err != nil {
		return nil, &readerIdentityError{"could not configure the Azure frontend artifact reader"}
	}
	return &azureWorkloadSource{client: client, repository: ref.Repository}, nil
}

func (s *azureWorkloadSource) Open(ctx context.Context, digest solutionhost.PayloadDigest) (io.ReadCloser, error) {
	response, err := s.client.GetBlob(ctx, s.repository, string(digest), nil)
	if err != nil {
		var status *azcore.ResponseError
		if errors.As(err, &status) {
			return nil, fmt.Errorf("%w: the registry answered HTTP %d", ErrUnavailable, status.StatusCode)
		}
		return nil, unavailable(err)
	}
	if response.DockerContentDigest == nil || *response.DockerContentDigest != string(digest) ||
		response.ContentLength == nil || *response.ContentLength < 0 || *response.ContentLength > MaxBundleBytes {
		response.BlobData.Close()
		return nil, errors.New("frontend artifact descriptor differs from the admitted bundle")
	}
	return response.BlobData, nil
}
