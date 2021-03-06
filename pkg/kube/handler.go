package kube

import (
	"encoding/json"
	"net/http"

	"github.com/gorilla/mux"
	"gopkg.in/asaskevich/govalidator.v8"

	"github.com/supergiant/supergiant/pkg/message"
	"github.com/supergiant/supergiant/pkg/sgerrors"
)

// Handler is a http controller for a kube entity.
type Handler struct {
	svc Interface
}

// NewHandler constructs a Handler for kubes.
func NewHandler(svc Interface) *Handler {
	return &Handler{svc}
}

// Register adds kube handlers to a router.
func (h *Handler) Register(r *mux.Router) {
	r.HandleFunc("/kubes", h.createKube).Methods(http.MethodPost)
	r.HandleFunc("/kubes", h.listKubes).Methods(http.MethodGet)
	r.HandleFunc("/kubes/{kname}", h.getKube).Methods(http.MethodGet)
	r.HandleFunc("/kubes/{kname}", h.deleteKube).Methods(http.MethodDelete)

	r.HandleFunc("/kubes/{kname}/resources", h.listResources).Methods(http.MethodGet)
	r.HandleFunc("/kubes/{kname}/resources/{resource}", h.getResource).Methods(http.MethodGet)

	r.HandleFunc("/kubes/{kname}/certs/{cname}", h.getCerts).Methods(http.MethodGet)
}

func (h *Handler) createKube(w http.ResponseWriter, r *http.Request) {
	k := &Kube{}
	err := json.NewDecoder(r.Body).Decode(k)
	if err != nil {
		message.SendInvalidJSON(w, err)
		return
	}

	ok, err := govalidator.ValidateStruct(k)
	if !ok {
		message.SendValidationFailed(w, err)
		return
	}

	if err = h.svc.Create(r.Context(), k); err != nil {
		message.SendUnknownError(w, err)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

func (h *Handler) getKube(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	kname := vars["kname"]

	k, err := h.svc.Get(r.Context(), kname)
	if err != nil {
		if sgerrors.IsNotFound(err) {
			message.SendNotFound(w, kname, err)
			return
		}
		message.SendUnknownError(w, err)
		return
	}

	if err = json.NewEncoder(w).Encode(k); err != nil {
		message.SendUnknownError(w, err)
	}
}

func (h *Handler) listKubes(w http.ResponseWriter, r *http.Request) {
	kubes, err := h.svc.ListAll(r.Context())
	if err != nil {
		message.SendUnknownError(w, err)
		return
	}

	if err = json.NewEncoder(w).Encode(kubes); err != nil {
		message.SendUnknownError(w, err)
	}
}

func (h *Handler) deleteKube(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	kname := vars["kname"]
	if err := h.svc.Delete(r.Context(), kname); err != nil {
		if sgerrors.IsNotFound(err) {
			message.SendNotFound(w, kname, err)
			return
		}
		message.SendUnknownError(w, err)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

func (h *Handler) listResources(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	kname := vars["kname"]

	rawResources, err := h.svc.ListKubeResources(r.Context(), kname)
	if err != nil {
		if sgerrors.IsNotFound(err) {
			message.SendNotFound(w, kname, err)
			return
		}
		message.SendUnknownError(w, err)
		return
	}

	if _, err = w.Write(rawResources); err != nil {
		message.SendUnknownError(w, err)
	}
}

func (h *Handler) getResource(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	kname := vars["kname"]
	rs := vars["resource"]
	ns := r.URL.Query().Get("namespace")
	name := r.URL.Query().Get("resourceName")

	rawResources, err := h.svc.GetKubeResources(r.Context(), kname, rs, ns, name)
	if err != nil {
		if sgerrors.IsNotFound(err) {
			message.SendNotFound(w, kname, err)
			return
		}
		message.SendUnknownError(w, err)
		return
	}

	if _, err = w.Write(rawResources); err != nil {
		message.SendUnknownError(w, err)
	}
}

func (h *Handler) getCerts(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	kname := vars["kname"]
	cname := vars["cname"]

	b, err := h.svc.GetCerts(r.Context(), kname, cname)
	if err != nil {
		if sgerrors.IsNotFound(err) {
			message.SendNotFound(w, kname, err)
			return
		}
		message.SendUnknownError(w, err)
		return
	}

	if err = json.NewEncoder(w).Encode(b); err != nil {
		message.SendUnknownError(w, err)
	}
}
