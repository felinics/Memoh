package errs

// Fault is who a failure is attributed to.
type Fault string

const (
	// FaultClient means the caller's request was refused by this process's rules.
	FaultClient Fault = "client"
	// FaultServer means this process failed: its code, data or configuration,
	// including a bad request this process sent downstream.
	FaultServer Fault = "server"
	// FaultDependency means an external provider, an internal downstream
	// service or the network failed.
	FaultDependency Fault = "dependency"
	// FaultCanceled means the caller canceled, or the caller's deadline passed.
	FaultCanceled Fault = "canceled"
)

func validFault(s string) (Fault, bool) {
	f := Fault(s)
	return f, f == FaultClient || f == FaultServer || f == FaultDependency || f == FaultCanceled
}
