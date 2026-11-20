package mobile

// holdAwake does nothing on Linux, where the platform holds no idle-sleep blocker.
func holdAwake() (release func()) { return func() {} }
