package computers

// sunPathSize is the room sockaddr_un has for a socket's path. libuv fills all of it, closing NUL or
// not, so a longer path a Node server binds is cut to this.
const sunPathSize = 104

// dialable is whether Go dials a path this long itself: it keeps room for the closing NUL here.
func dialable(path string) bool { return len(path) < sunPathSize }
