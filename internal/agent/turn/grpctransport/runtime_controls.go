package grpctransport

import (
	"context"
	"encoding/json"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/agent/turn/turnpb"
	"github.com/felinics/memoh/internal/apperror"
)

func (s *Server) RuntimeCommands(ctx context.Context, req *turnpb.JsonRequest) (*turnpb.JsonResponse, error) {
	var input turn.RuntimeControlRequest
	if err := json.Unmarshal(req.GetJson(), &input); err != nil || input.TeamID == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid runtime control request")
	}
	service, ok := s.service.(turn.RuntimeControlService)
	if !ok {
		return nil, status.Error(codes.Unimplemented, "runtime controls unavailable")
	}
	out, err := service.RuntimeCommands(ctx, input)
	if err != nil {
		return nil, s.runtimeControlError(err)
	}
	data, err := json.Marshal(out)
	if err != nil {
		return nil, s.mapError("runtime control response", err)
	}
	return &turnpb.JsonResponse{Json: data}, nil
}

func (c *Client) RuntimeCommands(ctx context.Context, input turn.RuntimeControlRequest) ([]turn.RuntimeCommand, error) {
	var out []turn.RuntimeCommand
	data, err := json.Marshal(input)
	if err != nil {
		return out, err
	}
	response, err := c.client.RuntimeCommands(ctx, &turnpb.JsonRequest{Json: data})
	if err != nil {
		return out, runtimeControlClientError(err)
	}
	err = json.Unmarshal(response.GetJson(), &out)
	return out, err
}

func (s *Server) RuntimeControls(ctx context.Context, req *turnpb.JsonRequest) (*turnpb.JsonResponse, error) {
	var input turn.RuntimeControlRequest
	if err := json.Unmarshal(req.GetJson(), &input); err != nil || input.TeamID == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid runtime control request")
	}
	service, ok := s.service.(turn.RuntimeControlService)
	if !ok {
		return nil, status.Error(codes.Unimplemented, "runtime controls unavailable")
	}
	out, err := service.RuntimeControls(ctx, input)
	if err != nil {
		return nil, s.runtimeControlError(err)
	}
	data, err := json.Marshal(out)
	if err != nil {
		return nil, s.mapError("runtime control response", err)
	}
	return &turnpb.JsonResponse{Json: data}, nil
}

func (c *Client) RuntimeControls(ctx context.Context, input turn.RuntimeControlRequest) (turn.RuntimeControls, error) {
	var out turn.RuntimeControls
	data, err := json.Marshal(input)
	if err != nil {
		return out, err
	}
	response, err := c.client.RuntimeControls(ctx, &turnpb.JsonRequest{Json: data})
	if err != nil {
		return out, runtimeControlClientError(err)
	}
	err = json.Unmarshal(response.GetJson(), &out)
	return out, err
}

func (s *Server) SetRuntimeMode(ctx context.Context, req *turnpb.JsonRequest) (*turnpb.JsonResponse, error) {
	var input turn.RuntimeControlRequest
	if err := json.Unmarshal(req.GetJson(), &input); err != nil || input.TeamID == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid runtime control request")
	}
	service, ok := s.service.(turn.RuntimeControlService)
	if !ok {
		return nil, status.Error(codes.Unimplemented, "runtime controls unavailable")
	}
	out, err := service.SetRuntimeMode(ctx, input)
	if err != nil {
		return nil, s.runtimeControlError(err)
	}
	data, err := json.Marshal(out)
	if err != nil {
		return nil, s.mapError("runtime control response", err)
	}
	return &turnpb.JsonResponse{Json: data}, nil
}

func (c *Client) SetRuntimeMode(ctx context.Context, input turn.RuntimeControlRequest) (turn.RuntimeModeState, error) {
	var out turn.RuntimeModeState
	data, err := json.Marshal(input)
	if err != nil {
		return out, err
	}
	response, err := c.client.SetRuntimeMode(ctx, &turnpb.JsonRequest{Json: data})
	if err != nil {
		return out, runtimeControlClientError(err)
	}
	err = json.Unmarshal(response.GetJson(), &out)
	return out, err
}

func (s *Server) ExecuteRuntimeCommand(ctx context.Context, req *turnpb.JsonRequest) (*turnpb.JsonResponse, error) {
	var input turn.RuntimeControlRequest
	if err := json.Unmarshal(req.GetJson(), &input); err != nil || input.TeamID == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid runtime control request")
	}
	service, ok := s.service.(turn.RuntimeControlService)
	if !ok {
		return nil, status.Error(codes.Unimplemented, "runtime controls unavailable")
	}
	out, err := service.ExecuteRuntimeCommand(ctx, input)
	if err != nil {
		return nil, s.runtimeControlError(err)
	}
	data, err := json.Marshal(out)
	if err != nil {
		return nil, s.mapError("runtime control response", err)
	}
	return &turnpb.JsonResponse{Json: data}, nil
}

func (c *Client) ExecuteRuntimeCommand(ctx context.Context, input turn.RuntimeControlRequest) (string, error) {
	var out string
	data, err := json.Marshal(input)
	if err != nil {
		return out, err
	}
	response, err := c.client.ExecuteRuntimeCommand(ctx, &turnpb.JsonRequest{Json: data})
	if err != nil {
		return out, runtimeControlClientError(err)
	}
	err = json.Unmarshal(response.GetJson(), &out)
	return out, err
}

const runtimeControlErrorPrefix = "memoh-runtime-control:"

func (s *Server) runtimeControlError(err error) error {
	if code := apperror.CodeOf(err); code != "" {
		return status.Error(codes.FailedPrecondition, runtimeControlErrorPrefix+string(code))
	}
	return s.mapError("runtime control", err)
}

func runtimeControlClientError(err error) error {
	if status.Code(err) == codes.FailedPrecondition && strings.HasPrefix(status.Convert(err).Message(), runtimeControlErrorPrefix) {
		return apperror.New(apperror.Code(strings.TrimPrefix(status.Convert(err).Message(), runtimeControlErrorPrefix)), nil)
	}
	return mapClientError(err)
}
