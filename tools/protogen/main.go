//go:build protogen

// Command protogen 用纯 Go 编译器（protocompile）编译 .proto 并调用
// protoc-gen-go / protoc-gen-go-grpc 生成 Go 代码，避免在 Windows 上安装 protoc。
//
// 用法：go run -tags protogen ./tools/protogen
package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/bufbuild/protocompile"
	"github.com/bufbuild/protocompile/protoutil"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"
)

const (
	protoRoot = "proto"
	outDir    = "gen/go"
	module    = "github.com/18395093096/classisland-mgmt-server"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "错误：", err)
		os.Exit(1)
	}
}

func run() error {
	files, err := collectProtoFiles(protoRoot)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("未在 %s 下找到 .proto 文件", protoRoot)
	}
	fmt.Printf("发现 %d 个 proto 文件，开始编译…\n", len(files))

	compiler := protocompile.Compiler{
		Resolver:       protocompile.WithStandardImports(&protocompile.SourceResolver{ImportPaths: []string{protoRoot}}),
		SourceInfoMode: protocompile.SourceInfoStandard,
	}
	results, err := compiler.Compile(context.Background(), files...)
	if err != nil {
		return fmt.Errorf("编译 proto 失败：%w", err)
	}

	var fdps []*descriptorpb.FileDescriptorProto
	for _, f := range results {
		fdp := protoutil.ProtoFromFileDescriptor(f)
		// 原始 proto 未声明 go_package，这里按目录推导并注入
		fdp.Options.GoPackage = proto.String(goPackage(fdp.GetName()))
		fdps = append(fdps, fdp)
	}

	base := &pluginpb.CodeGeneratorRequest{
		FileToGenerate:  files,
		ProtoFile:       fdps,
		CompilerVersion: &pluginpb.Version{Major: proto.Int32(6), Minor: proto.Int32(30), Patch: proto.Int32(2)},
	}

	steps := []struct {
		plugin string
		param  string
	}{
		{"protoc-gen-go", "paths=source_relative"},
		{"protoc-gen-go-grpc", "paths=source_relative,require_unimplemented_servers=false"},
	}
	for _, st := range steps {
		n, err := generate(base, st.plugin, st.param)
		if err != nil {
			return err
		}
		fmt.Printf("  %s 生成 %d 个文件\n", st.plugin, n)
	}
	fmt.Println("完成，输出目录：", outDir)
	return nil
}

func generate(base *pluginpb.CodeGeneratorRequest, plugin, param string) (int, error) {
	bin, err := lookupPlugin(plugin)
	if err != nil {
		return 0, err
	}
	req := proto.Clone(base).(*pluginpb.CodeGeneratorRequest)
	req.Parameter = proto.String(param)

	in, err := proto.Marshal(req)
	if err != nil {
		return 0, err
	}
	cmd := exec.Command(bin)
	cmd.Stdin = bytes.NewReader(in)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("运行 %s 失败：%w", bin, err)
	}
	var resp pluginpb.CodeGeneratorResponse
	if err := proto.Unmarshal(out, &resp); err != nil {
		return 0, err
	}
	if resp.Error != nil && *resp.Error != "" {
		return 0, fmt.Errorf("%s 报告错误：%s", plugin, *resp.Error)
	}
	for _, f := range resp.File {
		path := filepath.Join(outDir, filepath.FromSlash(f.GetName()))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return 0, err
		}
		if err := os.WriteFile(path, []byte(f.GetContent()), 0o644); err != nil {
			return 0, err
		}
	}
	return len(resp.File), nil
}

func lookupPlugin(name string) (string, error) {
	exe := name
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if p, err := exec.LookPath(exe); err == nil {
		return p, nil
	}
	gopath := os.Getenv("GOPATH")
	if gopath == "" {
		home, _ := os.UserHomeDir()
		gopath = filepath.Join(home, "go")
	}
	p := filepath.Join(gopath, "bin", exe)
	if _, err := os.Stat(p); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("找不到插件 %s，请先 go install google.golang.org/protobuf/cmd/%[1]s@latest", name)
}

func collectProtoFiles(root string) ([]string, error) {
	var out []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".proto") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	return out, err
}

// goPackage 由文件路径推导 go_package，如 Protobuf/Client/X.proto -> <module>/gen/go/Protobuf/Client;client
func goPackage(path string) string {
	dir := filepath.ToSlash(filepath.Dir(path))
	pkg := strings.ToLower(filepath.Base(dir))
	pkg = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		return -1
	}, pkg)
	if pkg == "" {
		pkg = "pb"
	}
	return module + "/" + outDir + "/" + dir + ";" + pkg
}
