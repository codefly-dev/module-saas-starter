package composition

import (
	"fmt"
	"slices"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
)

// This validates a consumer artifact; only Codefly produces the projection.
// The producer digest is checked separately, so removing a service never hides
// a stale source contract. Retained declarations must be byte-equivalent at the
// protobuf level and all references must resolve inside the selected contract.
func validateContractProjection(published, vendored []byte, services []string) error {
	var source, selected descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(published, &source); err != nil {
		return err
	}
	if err := proto.Unmarshal(vendored, &selected); err != nil {
		return err
	}
	if len(services) == 0 {
		if !proto.Equal(&source, &selected) {
			return fmt.Errorf("unselected contract differs from producer")
		}
		return nil
	}
	if _, err := protodesc.NewFiles(&selected); err != nil {
		return fmt.Errorf("projection does not resolve: %w", err)
	}
	originals := map[string]*descriptorpb.FileDescriptorProto{}
	files := map[string]*descriptorpb.FileDescriptorProto{}
	for _, file := range source.File {
		originals[file.GetName()] = file
	}
	found := map[string]bool{}
	var roots []string
	for _, file := range selected.File {
		original := originals[file.GetName()]
		if original == nil {
			return fmt.Errorf("undeclared file %s", file.GetName())
		}
		files[file.GetName()] = file
		for _, service := range file.Service {
			if !slices.Contains(services, service.GetName()) || found[service.GetName()] {
				return fmt.Errorf("unexpected or duplicate service %s", service.GetName())
			}
			found[service.GetName()] = true
			roots = append(roots, file.GetName())
		}
		if err := unchangedDeclarations(original.Service, file.Service); err != nil {
			return err
		}
		if err := unchangedDeclarations(original.MessageType, file.MessageType); err != nil {
			return err
		}
		if err := unchangedDeclarations(original.EnumType, file.EnumType); err != nil {
			return err
		}
		if err := unchangedDeclarations(original.Extension, file.Extension); err != nil {
			return err
		}
		for _, dependency := range file.Dependency {
			if !slices.Contains(original.Dependency, dependency) {
				return fmt.Errorf("%s adds dependency %s", file.GetName(), dependency)
			}
		}
		// Selection reindexes imports and comment locations. Everything else in
		// the file header (package, syntax, options, edition) must stay identical.
		header := func(f *descriptorpb.FileDescriptorProto) *descriptorpb.FileDescriptorProto {
			out := proto.Clone(f).(*descriptorpb.FileDescriptorProto)
			out.Service, out.MessageType, out.EnumType, out.Extension = nil, nil, nil, nil
			out.Dependency, out.PublicDependency, out.WeakDependency, out.SourceCodeInfo = nil, nil, nil, nil
			return out
		}
		if !proto.Equal(header(original), header(file)) {
			return fmt.Errorf("file header changed: %s", file.GetName())
		}
	}
	for _, name := range services {
		if !found[name] {
			return fmt.Errorf("selected service missing: %s", name)
		}
	}
	reached := map[string]bool{}
	var visit func(string)
	visit = func(name string) {
		if reached[name] {
			return
		}
		reached[name] = true
		for _, dependency := range files[name].Dependency {
			visit(dependency)
		}
	}
	for _, root := range roots {
		visit(root)
	}
	for name := range files {
		if !reached[name] {
			return fmt.Errorf("unrelated descriptor: %s", name)
		}
	}
	return nil
}

func unchangedDeclarations[T interface {
	proto.Message
	GetName() string
}](source, selected []T) error {
	for _, declaration := range selected {
		index := slices.IndexFunc(source, func(candidate T) bool { return candidate.GetName() == declaration.GetName() })
		if index < 0 || !proto.Equal(source[index], declaration) {
			return fmt.Errorf("declaration changed or added: %s", declaration.GetName())
		}
	}
	return nil
}

func TestContractProjectionRejectsAlteredAndUnrelatedDescriptors(t *testing.T) {
	source := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{{
		Name: proto.String("service.proto"), Package: proto.String("test"), Syntax: proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{Name: proto.String("Request"), Field: []*descriptorpb.FieldDescriptorProto{{Name: proto.String("value"), Number: proto.Int32(1), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()}}}},
		Service:     []*descriptorpb.ServiceDescriptorProto{{Name: proto.String("Selected"), Method: []*descriptorpb.MethodDescriptorProto{{Name: proto.String("Call"), InputType: proto.String(".test.Request"), OutputType: proto.String(".test.Request")}}}},
	}}}
	published, err := proto.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateContractProjection(published, published, []string{"Selected"}); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*descriptorpb.FileDescriptorSet){
		"removed field":   func(s *descriptorpb.FileDescriptorSet) { s.File[0].MessageType[0].Field = nil },
		"missing message": func(s *descriptorpb.FileDescriptorSet) { s.File[0].MessageType = nil },
		"missing service": func(s *descriptorpb.FileDescriptorSet) { s.File[0].Service = nil },
		"extra service": func(s *descriptorpb.FileDescriptorSet) {
			s.File[0].Service = append(s.File[0].Service, &descriptorpb.ServiceDescriptorProto{Name: proto.String("Other")})
		},
		"unrelated file": func(s *descriptorpb.FileDescriptorSet) {
			s.File = append(s.File, &descriptorpb.FileDescriptorProto{Name: proto.String("extra.proto"), Syntax: proto.String("proto3")})
		},
		"changed header": func(s *descriptorpb.FileDescriptorSet) {
			s.File[0].Options = &descriptorpb.FileOptions{GoPackage: proto.String("changed")}
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := proto.Clone(source).(*descriptorpb.FileDescriptorSet)
			mutate(changed)
			data, err := proto.Marshal(changed)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateContractProjection(published, data, []string{"Selected"}); err == nil {
				t.Fatal("accepted corrupted projection")
			}
		})
	}
}
