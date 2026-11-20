package computers

// sunPathSize is the room sockaddr_un has for a socket's path. libuv fills all of it, closing NUL or
// not, so a longer path a Node server binds is cut to this.
const sunPathSize = 108

// dialable is whether Go dials a path this long itself.
func dialable(path string) bool { return len(path) <= sunPathSize }
