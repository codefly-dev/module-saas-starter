package adapters

/* -----------------------------------------------------------------

STOPGAP (codefly-dev/service-go-grpc#223): this file is hand-maintained.
It is not the go-grpc agent's output, although its name and layout follow the
agent's template. The agent renders these adapters only for a service whose
proto declares a single AccountsService. Accounts serves many proto services
and declares no AccountsService, so the agent's Sync never writes this file
(go-grpc 0.1.72, generatedScaffoldTargets). It stays hand-maintained until #223
lets the agent render this service's shape; adopt the generated file then,
keeping the CUSTOM lines. main.go and plugins/registry.go carry the same
stopgap.

----------------------------------------------------------------- */

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
)

type Server struct {
	Grpc    *GrpcServer
	Rest    *RestServer
	Connect *ConnectServer
}

func NewServer(config *Configuration, grpcOpts ...grpc.ServerOption) (*Server, error) {

	grpc, err := NewGrpServer(config, grpcOpts...)
	if err != nil {
		return nil, err
	}

	var rest *RestServer
	if config.EndpointHttpPort != nil {
		rest, err = NewRestServer(config, grpc)
		if err != nil {
			return nil, err
		}
	}

	var conn *ConnectServer
	if config.EndpointConnectPort != nil {
		conn, err = NewConnectServer(config, grpc)
		if err != nil {
			return nil, err
		}
	}

	return &Server{
		Grpc:    grpc,
		Rest:    rest,
		Connect: conn,
	}, nil
}

func (server *Server) Start(ctx context.Context) error {
	if server.Rest != nil {
		go func() {
			err := server.Rest.Run(ctx)
			if err != nil {
				panic(err)
			}
		}()
	}
	if server.Grpc.configuration.EndpointAuthorityPort != nil {
		go func() {
			if err := server.Grpc.RunAuthority(ctx); err != nil {
				panic(err)
			}
		}()
	}
	if server.Connect != nil {
		go func() {
			err := server.Connect.Run(ctx)
			if err != nil {
				panic(err)
			}
		}()
	}
	return server.Grpc.Run(ctx)
}

func (server *Server) Stop() {
	fmt.Println("Stopping server...")
	server.Grpc.gRPC.GracefulStop()
	server.Grpc.internalGRPC.GracefulStop()
	server.Grpc.authorityGRPC.GracefulStop()
}
