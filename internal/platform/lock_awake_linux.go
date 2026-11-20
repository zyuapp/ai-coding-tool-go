package platform

// KeepsLockedMachineAwake is false on Linux, where the app does not hold a locked machine awake.
const KeepsLockedMachineAwake = false

// NewLockAwake does nothing on Linux; its result's methods are safe to call.
func NewLockAwake(func() bool) *LockAwake { return nil }
