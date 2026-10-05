package rpc

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/errs"
)

// Decode is what an RPC client returns for err, received from a call. Every
// internal RPC client decodes with it, so that a received failure is restored
// by the same rules everywhere:
//
//   - The result is marked remote and keeps the received status on its chain,
//     where diagnostics read the status and the fault the server wrote.
//   - A status is restored, in order, to the sentinel reasons registers for
//     its ErrorInfo reason, to the catalog apperror of a reason that is a
//     catalog code, or to the sentinel byCode holds for its code. A status
//     none of them restores is returned under the remote marker. No step reads
//     the status message.
//   - Canceled and DeadlineExceeded stay statuses. Whether the call ended
//     because its caller did is answered by the caller's own context, not by
//     the code: a server, a closing connection and the caller all end a call
//     with Canceled.
//
// A sentinel restored by code reads as the sentinel followed by the received
// message, which is the only description such a status has. An error without
// a status, such as the io.EOF a stream reports when it ends, was not received
// and is returned unchanged.
func Decode(err error, reasons Reasons, byCode map[codes.Code]error) error {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok {
		return err
	}
	if restored := reasons.Decode(err); restored != nil {
		return restored
	}
	if restored := DecodeAppError(err); restored != nil {
		return restored
	}
	if sentinel, ok := byCode[st.Code()]; ok {
		return Restored(WithAdapterMessage(sentinel, sentinel.Error()+": "+st.Message()), err)
	}
	return errs.Remote(err)
}

// Forward marks a decoded catalog error as the answer to end-user input this
// process forwarded, so that a client fault the server reported stays a
// client fault here and the error is answered to the end user as it would be
// in one process. A client that relays a user's request decodes its results
// through Forward; any other error is returned unchanged, since a status
// outside the catalog refuses what this process sent, not what the user did.
func Forward(err error) error {
	if apperror.CodeOf(err) == "" {
		return err
	}
	return errs.Forwarded(err)
}
