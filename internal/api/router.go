package api

import (
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/ravi-chuppala/vpc-routing/internal/auth"
	"github.com/ravi-chuppala/vpc-routing/internal/model"
	"github.com/ravi-chuppala/vpc-routing/internal/store"
	"github.com/ravi-chuppala/vpc-routing/internal/vni"
)

// Router is the HTTP API router.
type Router struct {
	mux     *http.ServeMux
	vpc     *VPCHandler
	peering *PeeringHandler
	limiter *auth.RateLimiter
}

// NewRouter creates a new API router with all handlers wired.
func NewRouter(vpcs store.VPCStore, peerings store.PeeringStore, events store.EventStore, routes store.RouteStore, alloc vni.VNIAllocator) *Router {
	r := &Router{
		mux:     http.NewServeMux(),
		vpc:     NewVPCHandler(vpcs, peerings, alloc),
		peering: NewPeeringHandler(vpcs, peerings, events, routes),
		limiter: auth.NewRateLimiter(auth.DefaultRateLimitConfig()),
	}
	r.registerRoutes()
	return r
}

func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rt.mux.ServeHTTP(w, r)
}

func (rt *Router) registerRoutes() {
	// VPC endpoints
	rt.mux.HandleFunc("/v1/vpcs", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			rt.rateLimitMutate(w, r, rt.vpc.Create)
		case http.MethodGet:
			rt.rateLimitRead(w, r, rt.vpc.List)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	rt.mux.HandleFunc("/v1/vpcs/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/v1/vpcs/")
		parts := strings.Split(path, "/")
		vpcID, err := uuid.Parse(parts[0])
		if err != nil {
			writeError(w, model.ErrInvalidInput("invalid VPC ID"))
			return
		}

		// /v1/vpcs/{id}/effective-routes
		if len(parts) == 2 && parts[1] == "effective-routes" && r.Method == http.MethodGet {
			rt.rateLimitRead(w, r, func(w http.ResponseWriter, r *http.Request) {
				rt.peering.GetEffectiveRoutes(w, r, vpcID)
			})
			return
		}

		// /v1/vpcs/{id}
		if len(parts) == 1 {
			switch r.Method {
			case http.MethodGet:
				rt.rateLimitRead(w, r, func(w http.ResponseWriter, r *http.Request) {
					rt.vpc.Get(w, r, vpcID)
				})
			case http.MethodDelete:
				rt.rateLimitMutate(w, r, func(w http.ResponseWriter, r *http.Request) {
					rt.vpc.Delete(w, r, vpcID)
				})
			default:
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			}
			return
		}

		http.NotFound(w, r)
	})

	// Peering endpoints
	rt.mux.HandleFunc("/v1/peerings", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			rt.rateLimitMutate(w, r, rt.peering.Create)
		case http.MethodGet:
			rt.rateLimitRead(w, r, rt.peering.List)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	rt.mux.HandleFunc("/v1/peerings/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/v1/peerings/")
		parts := strings.Split(path, "/")
		peeringID, err := uuid.Parse(parts[0])
		if err != nil {
			writeError(w, model.ErrInvalidInput("invalid peering ID"))
			return
		}

		// Sub-resources
		if len(parts) >= 2 {
			switch parts[1] {
			case "accept":
				if r.Method == http.MethodPost {
					rt.rateLimitMutate(w, r, func(w http.ResponseWriter, r *http.Request) {
						rt.peering.Accept(w, r, peeringID)
					})
					return
				}
			case "reject":
				if r.Method == http.MethodPost {
					rt.rateLimitMutate(w, r, func(w http.ResponseWriter, r *http.Request) {
						rt.peering.Reject(w, r, peeringID)
					})
					return
				}
			case "routes":
				switch r.Method {
				case http.MethodGet:
					rt.rateLimitRead(w, r, func(w http.ResponseWriter, r *http.Request) {
						rt.peering.ListRoutes(w, r, peeringID)
					})
					return
				case http.MethodPost:
					rt.rateLimitMutate(w, r, func(w http.ResponseWriter, r *http.Request) {
						rt.peering.OverrideRoute(w, r, peeringID)
					})
					return
				}
			case "events":
				if r.Method == http.MethodGet {
					rt.rateLimitRead(w, r, func(w http.ResponseWriter, r *http.Request) {
						rt.peering.ListEvents(w, r, peeringID)
					})
					return
				}
			}
			http.NotFound(w, r)
			return
		}

		// /v1/peerings/{id}
		switch r.Method {
		case http.MethodGet:
			rt.rateLimitRead(w, r, func(w http.ResponseWriter, r *http.Request) {
				rt.peering.Get(w, r, peeringID)
			})
		case http.MethodPatch:
			rt.rateLimitMutate(w, r, func(w http.ResponseWriter, r *http.Request) {
				rt.peering.Update(w, r, peeringID)
			})
		case http.MethodDelete:
			rt.rateLimitMutate(w, r, func(w http.ResponseWriter, r *http.Request) {
				rt.peering.Delete(w, r, peeringID)
			})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Health endpoints
	rt.mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})
}

func (rt *Router) rateLimitMutate(w http.ResponseWriter, r *http.Request, handler http.HandlerFunc) {
	accountID, err := auth.AccountFromContext(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	if !rt.limiter.AllowMutate(accountID) {
		writeError(w, model.ErrRateLimited())
		return
	}
	handler(w, r)
}

func (rt *Router) rateLimitRead(w http.ResponseWriter, r *http.Request, handler http.HandlerFunc) {
	accountID, err := auth.AccountFromContext(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	if !rt.limiter.AllowRead(accountID) {
		writeError(w, model.ErrRateLimited())
		return
	}
	handler(w, r)
}
