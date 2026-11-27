package adminapi

import "time"

func (h *Handler) sessionPolicy() (time.Duration, time.Duration) {
	if h.runtime != nil {
		if current := h.runtime.Current(); current != nil {
			return current.WebSessionDurations()
		}
	}
	return h.sessionTTL, h.sessionIdle
}

func (h *Handler) anonymousAllowed() bool {
	return len(h.anonymousActions()) > 0
}

func (h *Handler) anonymousActions() []string {
	if h.runtime != nil {
		if current := h.runtime.Current(); current != nil {
			if !current.RegistryAuth.AllowAnonymous {
				return []string{}
			}
			return append([]string(nil), current.RegistryAuth.AnonymousActions...)
		}
	}
	if !h.allowAnonymous {
		return []string{}
	}
	return []string{"pull"}
}

func (h *Handler) proxyDisabled() bool {
	if h.runtime != nil {
		if current := h.runtime.Current(); current != nil {
			return current.Proxy.Disabled
		}
	}
	return false
}
