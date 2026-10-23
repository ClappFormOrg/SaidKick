package server

import "github.com/labstack/echo/v4"

// Echo exposes the underlying Echo instance to the external server_test
// package. It is compiled only into test binaries.
func (s *Server) Echo() *echo.Echo { return s.echo }
