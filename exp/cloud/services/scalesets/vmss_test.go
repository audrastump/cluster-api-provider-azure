/*
Copyright 2020 The Kubernetes Authors.

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

package scalesets

import (
	"context"
	"fmt"
	"testing"

	"github.com/Azure/azure-sdk-for-go/profiles/latest/compute/mgmt/compute"
	"github.com/Azure/go-autorest/autorest"
	"github.com/Azure/go-autorest/autorest/to"
	"github.com/golang/mock/gomock"
	"github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1alpha3"
	clusterv1exp "sigs.k8s.io/cluster-api/exp/api/v1alpha3"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	infrav1 "sigs.k8s.io/cluster-api-provider-azure/api/v1alpha3"
	azure "sigs.k8s.io/cluster-api-provider-azure/cloud"
	"sigs.k8s.io/cluster-api-provider-azure/cloud/scope"
	infrav1exp "sigs.k8s.io/cluster-api-provider-azure/exp/api/v1alpha3"
	scopeExp "sigs.k8s.io/cluster-api-provider-azure/exp/cloud/scope"
	"sigs.k8s.io/cluster-api-provider-azure/exp/cloud/services/scalesets/mock_scalesets"
	"sigs.k8s.io/cluster-api-provider-azure/internal/test/matchers"
)

func init() {
	_ = clusterv1.AddToScheme(scheme.Scheme)
}

func TestNewService(t *testing.T) {
	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster"},
	}
	client := fake.NewFakeClientWithScheme(scheme.Scheme, cluster)
	s, err := scope.NewClusterScope(scope.ClusterScopeParams{
		AzureClients: scope.AzureClients{
			Authorizer: autorest.NullAuthorizer{},
		},
		Client:  client,
		Cluster: cluster,
		AzureCluster: &infrav1.AzureCluster{
			Spec: infrav1.AzureClusterSpec{
				Location: "test-location",
				ResourceGroup:  "my-rg",
				SubscriptionID: "123",
				NetworkSpec: infrav1.NetworkSpec{
					Vnet: infrav1.VnetSpec{Name: "my-vnet", ResourceGroup: "my-rg"},
				},
			},
		},
	})
	g := gomega.NewGomegaWithT(t)
	g.Expect(err).ToNot(gomega.HaveOccurred())

	mps, err := scopeExp.NewMachinePoolScope(scopeExp.MachinePoolScopeParams{
		AzureClients:     s.AzureClients,
		Client:           client,
		Logger:           s.Logger,
		Cluster:          s.Cluster,
		MachinePool:      new(clusterv1exp.MachinePool),
		AzureCluster:     s.AzureCluster,
		AzureMachinePool: new(infrav1exp.AzureMachinePool),
	})
	g.Expect(err).ToNot(gomega.HaveOccurred())
	actual := NewService(s, mps)
	g.Expect(actual).ToNot(gomega.BeNil())
	g.Expect(actual.MachinePoolScope).To(gomega.Equal(mps))
	g.Expect(actual.Scope).To(gomega.Equal(s))
}

func TestService_Get(t *testing.T) {
	cases := []struct {
		Name        string
		SpecFactory func(g *gomega.GomegaWithT, svc *Service) interface{}
		Setup       func(ctx context.Context, g *gomega.GomegaWithT, svc *Service)
		Expect      func(ctx context.Context, g *gomega.GomegaWithT, result interface{}, err error)
	}{
		{
			Name: "WithInvalidSepcType",
			SpecFactory: func(g *gomega.GomegaWithT, _ *Service) interface{} {
				return "bin"
			},
			Expect: func(_ context.Context, g *gomega.GomegaWithT, result interface{}, err error) {
				g.Expect(err).To(gomega.MatchError("invalid VMSS specification"))
			},
		},
		{
			Name: "WithValidSpecBut404FromAzureOnVMSS",
			SpecFactory: func(g *gomega.GomegaWithT, svc *Service) interface{} {
				return &Spec{
					Name: svc.MachinePoolScope.Name(),
				}
			},
			Setup: func(ctx context.Context, g *gomega.GomegaWithT, svc *Service) {
				mockCtrl := gomock.NewController(t)
				vmssMock := mock_scalesets.NewMockClient(mockCtrl)
				svc.Client = vmssMock
				vmssMock.EXPECT().Get(gomock.Any(), svc.Scope.AzureCluster.Spec.ResourceGroup, svc.MachinePoolScope.Name()).Return(compute.VirtualMachineScaleSet{}, autorest.DetailedError{
					StatusCode: 404,
				})
			},
			Expect: func(ctx context.Context, g *gomega.GomegaWithT, result interface{}, err error) {
				g.Expect(err).To(gomega.Equal(autorest.DetailedError{
					StatusCode: 404,
				}))
			},
		},
		{
			Name: "WithValidSpecBut404FromAzureOnInstances",
			SpecFactory: func(g *gomega.GomegaWithT, svc *Service) interface{} {
				return &Spec{
					Name: svc.MachinePoolScope.Name(),
				}
			},
			Setup: func(ctx context.Context, g *gomega.GomegaWithT, svc *Service) {
				mockCtrl := gomock.NewController(t)
				vmssMock := mock_scalesets.NewMockClient(mockCtrl)
				svc.Client = vmssMock
				vmssMock.EXPECT().Get(gomock.Any(), svc.Scope.AzureCluster.Spec.ResourceGroup, svc.MachinePoolScope.Name()).Return(compute.VirtualMachineScaleSet{}, nil)
				vmssMock.EXPECT().ListInstances(gomock.Any(), svc.Scope.AzureCluster.Spec.ResourceGroup, svc.MachinePoolScope.Name()).Return([]compute.VirtualMachineScaleSetVM{}, autorest.DetailedError{
					StatusCode: 404,
				})
			},
			Expect: func(ctx context.Context, g *gomega.GomegaWithT, result interface{}, err error) {
				g.Expect(err).To(gomega.Equal(autorest.DetailedError{
					StatusCode: 404,
				}))
			},
		},
		{
			Name: "WithValidSpecWithVMSSAndInstancesReturned",
			SpecFactory: func(g *gomega.GomegaWithT, svc *Service) interface{} {
				return &Spec{
					Name: svc.MachinePoolScope.Name(),
				}
			},
			Setup: func(ctx context.Context, g *gomega.GomegaWithT, svc *Service) {
				mockCtrl := gomock.NewController(t)
				vmssMock := mock_scalesets.NewMockClient(mockCtrl)
				svc.Client = vmssMock
				vmssMock.EXPECT().Get(gomock.Any(), svc.Scope.AzureCluster.Spec.ResourceGroup, svc.MachinePoolScope.Name()).Return(compute.VirtualMachineScaleSet{
					Name: to.StringPtr(svc.MachinePoolScope.Name()),
					Sku: &compute.Sku{
						Capacity: to.Int64Ptr(1),
						Name:     to.StringPtr("Standard"),
					},
					VirtualMachineScaleSetProperties: &compute.VirtualMachineScaleSetProperties{
						ProvisioningState: to.StringPtr("Succeeded"),
					},
				}, nil)
				vmssMock.EXPECT().ListInstances(gomock.Any(), svc.Scope.AzureCluster.Spec.ResourceGroup, svc.MachinePoolScope.Name()).Return([]compute.VirtualMachineScaleSetVM{
					{
						Name:       to.StringPtr("vm0"),
						InstanceID: to.StringPtr("0"),
						VirtualMachineScaleSetVMProperties: &compute.VirtualMachineScaleSetVMProperties{
							ProvisioningState: to.StringPtr("Succeeded"),
						},
					},
				}, nil)
			},
			Expect: func(ctx context.Context, g *gomega.GomegaWithT, result interface{}, err error) {
				g.Expect(result).To(gomega.Equal(&infrav1exp.VMSS{
					Name:     "capz-mp-0",
					Sku:      "Standard",
					Capacity: 1,
					Image:    infrav1.Image{},
					State:    "Succeeded",
					Instances: []infrav1exp.VMSSVM{
						{
							InstanceID: "0",
							Name:       "vm0",
							State:      "Succeeded",
						},
					},
				}))
			},
		},
	}

	for _, c := range cases {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			t.Parallel()
			g := gomega.NewGomegaWithT(t)
			svc := getNewService(g)
			spec := c.SpecFactory(g, svc)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if c.Setup != nil {
				c.Setup(ctx, g, svc)
			}
			res, err := svc.Get(context.Background(), spec)
			c.Expect(ctx, g, res, err)
		})
	}
}

func TestService_Reconcile(t *testing.T) {
	cases := []struct {
		Name        string
		SpecFactory func(g *gomega.GomegaWithT, svc *Service) interface{}
		Setup       func(ctx context.Context, g *gomega.GomegaWithT, svc *Service, spec *Spec)
		Expect      func(ctx context.Context, g *gomega.GomegaWithT, err error)
	}{
		{
			Name: "WithInvalidSepcType",
			SpecFactory: func(g *gomega.GomegaWithT, _ *Service) interface{} {
				return "bazz"
			},
			Expect: func(_ context.Context, g *gomega.GomegaWithT, err error) {
				g.Expect(err).To(gomega.MatchError("invalid VMSS specification"))
			},
		},
		{
			Name: "WithValidSpec",
			SpecFactory: func(g *gomega.GomegaWithT, svc *Service) interface{} {
				return &Spec{
					Name:       svc.MachinePoolScope.Name(),
					Sku:        "skuName",
					Capacity:   2,
					SSHKeyData: "sshKeyData",
					OSDisk: infrav1.OSDisk{
						OSType:     "Linux",
						DiskSizeGB: 120,
						ManagedDisk: infrav1.ManagedDisk{
							StorageAccountType: "accountType",
						},
					},
					Image: &infrav1.Image{
						ID: to.StringPtr("image"),
					},
					CustomData: "customData",
				}
			},
			Setup: func(ctx context.Context, g *gomega.GomegaWithT, svc *Service, spec *Spec) {
				mockCtrl := gomock.NewController(t)
				vmssMock := mock_scalesets.NewMockClient(mockCtrl)
				svc.Client = vmssMock

				storageProfile, err := generateStorageProfile(*spec)
				g.Expect(err).ToNot(gomega.HaveOccurred())

				vmss := compute.VirtualMachineScaleSet{
					Location: to.StringPtr(svc.Scope.Location()),
					Tags: map[string]*string{
						"Name":                            to.StringPtr("capz-mp-0"),
						"kubernetes.io_cluster_capz-mp-0": to.StringPtr("owned"),
						"sigs.k8s.io_cluster-api-provider-azure_cluster_test-cluster": to.StringPtr("owned"),
						"sigs.k8s.io_cluster-api-provider-azure_role":                 to.StringPtr("node"),
					},
					Sku: &compute.Sku{
						Name:     to.StringPtr(spec.Sku),
						Tier:     to.StringPtr("Standard"),
						Capacity: to.Int64Ptr(spec.Capacity),
					},
					VirtualMachineScaleSetProperties: &compute.VirtualMachineScaleSetProperties{
						UpgradePolicy: &compute.UpgradePolicy{
							Mode: compute.Manual,
						},
						VirtualMachineProfile: &compute.VirtualMachineScaleSetVMProfile{
							OsProfile: &compute.VirtualMachineScaleSetOSProfile{
								ComputerNamePrefix: to.StringPtr(spec.Name),
								AdminUsername:      to.StringPtr(azure.DefaultUserName),
								CustomData:         to.StringPtr(spec.CustomData),
								LinuxConfiguration: &compute.LinuxConfiguration{
									SSH: &compute.SSHConfiguration{
										PublicKeys: &[]compute.SSHPublicKey{
											{
												Path:    to.StringPtr(fmt.Sprintf("/home/%s/.ssh/authorized_keys", azure.DefaultUserName)),
												KeyData: to.StringPtr(spec.SSHKeyData),
											},
										},
									},
									DisablePasswordAuthentication: to.BoolPtr(true),
								},
							},
							StorageProfile: storageProfile,
							NetworkProfile: &compute.VirtualMachineScaleSetNetworkProfile{
								NetworkInterfaceConfigurations: &[]compute.VirtualMachineScaleSetNetworkConfiguration{
									{
										Name: to.StringPtr(spec.Name + "-netconfig"),
										VirtualMachineScaleSetNetworkConfigurationProperties: &compute.VirtualMachineScaleSetNetworkConfigurationProperties{
											Primary:            to.BoolPtr(true),
											EnableIPForwarding: to.BoolPtr(true),
											IPConfigurations: &[]compute.VirtualMachineScaleSetIPConfiguration{
												{
													Name: to.StringPtr(spec.Name + "-ipconfig"),
													VirtualMachineScaleSetIPConfigurationProperties: &compute.VirtualMachineScaleSetIPConfigurationProperties{
														Subnet: &compute.APIEntityReference{
															ID: to.StringPtr(svc.Scope.AzureCluster.Spec.NetworkSpec.Subnets[0].ID),
														},
														Primary:                 to.BoolPtr(true),
														PrivateIPAddressVersion: compute.IPv4,
													},
												},
											},
										},
									},
								},
							},
						},
					},
				}

				vmssMock.EXPECT().CreateOrUpdate(gomock.Any(), svc.Scope.AzureCluster.Spec.ResourceGroup, spec.Name, matchers.DiffEq(vmss)).Return(nil)
			},
			Expect: func(ctx context.Context, g *gomega.GomegaWithT, err error) {
				g.Expect(err).ToNot(gomega.HaveOccurred())
			},
		},
	}

	for _, c := range cases {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			t.Parallel()
			g := gomega.NewGomegaWithT(t)
			svc := getNewService(g)
			spec := c.SpecFactory(g, svc)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if c.Setup != nil {
				c.Setup(ctx, g, svc, spec.(*Spec))
			}
			err := svc.Reconcile(context.Background(), spec)
			c.Expect(ctx, g, err)
		})
	}
}

func TestService_Delete(t *testing.T) {
	cases := []struct {
		Name        string
		SpecFactory func(g *gomega.GomegaWithT, svc *Service) interface{}
		Setup       func(ctx context.Context, g *gomega.GomegaWithT, svc *Service)
		Expect      func(ctx context.Context, g *gomega.GomegaWithT, err error)
	}{
		{
			Name: "WithInvalidSepcType",
			SpecFactory: func(g *gomega.GomegaWithT, _ *Service) interface{} {
				return "foo"
			},
			Expect: func(_ context.Context, g *gomega.GomegaWithT, err error) {
				g.Expect(err).To(gomega.MatchError("invalid VMSS specification"))
			},
		},
		{
			Name: "WithValidSpecBut404FromAzureOnVMSSAssumeAlreadyDeleted",
			SpecFactory: func(g *gomega.GomegaWithT, svc *Service) interface{} {
				return &Spec{
					Name: svc.MachinePoolScope.Name(),
				}
			},
			Setup: func(ctx context.Context, g *gomega.GomegaWithT, svc *Service) {
				mockCtrl := gomock.NewController(t)
				vmssMock := mock_scalesets.NewMockClient(mockCtrl)
				svc.Client = vmssMock
				vmssMock.EXPECT().Delete(gomock.Any(), svc.Scope.AzureCluster.Spec.ResourceGroup, svc.MachinePoolScope.Name()).Return(autorest.DetailedError{
					StatusCode: 404,
				})
			},
			Expect: func(ctx context.Context, g *gomega.GomegaWithT, err error) {
				g.Expect(err).ToNot(gomega.HaveOccurred())
			},
		},
		{
			Name: "WithValidSpecAndSuccessfulDelete",
			SpecFactory: func(g *gomega.GomegaWithT, svc *Service) interface{} {
				return &Spec{
					Name: svc.MachinePoolScope.Name(),
				}
			},
			Setup: func(ctx context.Context, g *gomega.GomegaWithT, svc *Service) {
				mockCtrl := gomock.NewController(t)
				vmssMock := mock_scalesets.NewMockClient(mockCtrl)
				svc.Client = vmssMock
				vmssMock.EXPECT().Delete(gomock.Any(), svc.Scope.AzureCluster.Spec.ResourceGroup, svc.MachinePoolScope.Name()).Return(nil)
			},
			Expect: func(ctx context.Context, g *gomega.GomegaWithT, err error) {
				g.Expect(err).ToNot(gomega.HaveOccurred())
			},
		},
	}

	for _, c := range cases {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			t.Parallel()
			g := gomega.NewGomegaWithT(t)
			svc := getNewService(g)
			spec := c.SpecFactory(g, svc)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if c.Setup != nil {
				c.Setup(ctx, g, svc)
			}
			err := svc.Delete(context.Background(), spec)
			c.Expect(ctx, g, err)
		})
	}
}

func getNewService(g *gomega.GomegaWithT) *Service {
	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster"},
	}
	client := fake.NewFakeClientWithScheme(scheme.Scheme, cluster)
	s, err := scope.NewClusterScope(scope.ClusterScopeParams{
		AzureClients: scope.AzureClients{
			Authorizer: autorest.NullAuthorizer{},
		},
		Client:  client,
		Cluster: cluster,
		AzureCluster: &infrav1.AzureCluster{
			Spec: infrav1.AzureClusterSpec{
				Location: "test-location",
				ResourceGroup:  "my-rg",
				SubscriptionID: "123",
				NetworkSpec: infrav1.NetworkSpec{
					Vnet: infrav1.VnetSpec{Name: "my-vnet", ResourceGroup: "my-rg"},
					Subnets: infrav1.Subnets{
						{
							ID: "subnet0.id",
						},
					},
				},
			},
		},
	})
	g.Expect(err).ToNot(gomega.HaveOccurred())

	mps, err := scopeExp.NewMachinePoolScope(scopeExp.MachinePoolScopeParams{
		AzureClients: s.AzureClients,
		Client:       client,
		Logger:       s.Logger,
		Cluster:      s.Cluster,
		MachinePool:  new(clusterv1exp.MachinePool),
		AzureCluster: s.AzureCluster,
		AzureMachinePool: &infrav1exp.AzureMachinePool{
			ObjectMeta: metav1.ObjectMeta{
				Name: "capz-mp-0",
			},
		},
	})
	g.Expect(err).ToNot(gomega.HaveOccurred())

	return NewService(s, mps)
}
