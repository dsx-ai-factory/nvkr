package factory

import authored "github.com/NVIDIA/nke-recipes/pkg/nkd/definition"

const fixtureUtilityContainer = "golang@sha256:fe5d57d3b718e7a4986bae156c2d73f44973bfd313073aed08a4de6692bb6161"

func fixtureInputs(name, output, runsOn string) (authored.Definition, authored.Environment) {
	return authored.Definition{APIVersion: authored.DefinitionAPIVersion, Kind: authored.DefinitionKind,
			Metadata: authored.Metadata{Name: name}, Catalog: authored.Catalog{Base: "testdata/catalogs/kubernetes-layered"},
			Outputs: []authored.DefinitionOutput{{Name: output, Platform: "linux/amd64", Format: authored.OutputExecutable, Components: []string{"containerd", "crictl", "kubeadm", "kubectl", "kubelet", "runc"}}}},
		authored.Environment{APIVersion: authored.EnvironmentAPIVersion, Kind: authored.EnvironmentKind, Metadata: authored.Metadata{Name: "fixture"}, RunsOn: runsOn, UtilityContainer: fixtureUtilityContainer, UtilityPlatform: "linux/amd64"}
}
