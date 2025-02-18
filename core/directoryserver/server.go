package directoryserver

import (
	"crypto/ecdsa"
	"crypto/x509"
	"errors"
	"github.com/arcsecc/lohpi/core/comm"
	pb "github.com/arcsecc/lohpi/protobuf"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"log"
	"net"
	"time"
)

var (
	errNilCert = errors.New("Given certificate was nil")
	errNilPriv = errors.New("Given private key was nil")
)

type gRPCServer struct {
	rpcServer *grpc.Server

	listener   net.Listener
	listenAddr string
}

func newDirectoryGRPCServer(cert, caCert *x509.Certificate, priv *ecdsa.PrivateKey, l net.Listener) (*gRPCServer, error) {
	var serverOpts []grpc.ServerOption
	if cert == nil {
		return nil, errNilCert
	}

	if priv == nil {
		return nil, errNilPriv
	}

	serverConf := comm.ServerConfig(cert, caCert, priv)

	keepAlive := keepalive.ServerParameters{
		MaxConnectionIdle: time.Minute * 5,
		Time:              time.Minute * 5,
	}

	creds := credentials.NewTLS(serverConf)

	serverOpts = append(serverOpts, grpc.Creds(creds))
	serverOpts = append(serverOpts, grpc.KeepaliveParams(keepAlive))

	return &gRPCServer{
		listener:   l,
		listenAddr: l.Addr().String(),
		rpcServer:  grpc.NewServer(serverOpts...),
	}, nil
}

func (s *gRPCServer) Register(p pb.DirectoryServerServer) {
	pb.RegisterDirectoryServerServer(s.rpcServer, p)
}

func (s *gRPCServer) Start() {
	s.start()
}

func (s *gRPCServer) Stop() {
	s.stop()
}

func (s *gRPCServer) Addr() string {
	return s.addr()
}

func (s *gRPCServer) start() error {
	err := s.rpcServer.Serve(s.listener)
	if err != nil {
		log.Println(err.Error())
	}

	return err
}

func (s *gRPCServer) stop() {
	s.rpcServer.Stop()
}

func (s *gRPCServer) addr() string {
	return s.listenAddr
}
