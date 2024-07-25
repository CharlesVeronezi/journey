package api

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/go-playground/validator/v10"
	"github.com/jackc/pgx/v5"
	"journey/internal/api/spec"
	"journey/internal/pgstore"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

type mailer interface {
	SendConfirmTripEmailToTripOwner(uuid.UUID) error
}

type store interface {
	CreateTrip(ctx context.Context, pool *pgxpool.Pool, params spec.CreateTripRequest) (uuid.UUID, error)
	GetParticipant(ctx context.Context, participantID uuid.UUID) (pgstore.Participant, error)
	ConfirmParticipant(ctx context.Context, participantID uuid.UUID) error
	GetTrip(ctx context.Context, id uuid.UUID) (pgstore.Trip, error)
	GetTripActivities(ctx context.Context, id uuid.UUID) ([]pgstore.Activity, error)
}

type ApiServer struct {
	store     store
	logger    *zap.Logger
	validator *validator.Validate
	pool      *pgxpool.Pool
	mailer    mailer
}

func NewAPI(poll *pgxpool.Pool, logger *zap.Logger, mailer mailer) ApiServer {
	validator := validator.New()
	return ApiServer{pgstore.New(poll), logger, validator, poll, mailer}
}

// PatchParticipantsParticipantIDConfirm Confirms a participant on a trip.
// (PATCH /participants/{participantId}/confirm)
func (api ApiServer) PatchParticipantsParticipantIDConfirm(w http.ResponseWriter, r *http.Request, participantID string) *spec.Response {
	id, err := uuid.Parse(participantID)
	if err != nil {
		return spec.PatchParticipantsParticipantIDConfirmJSON400Response(spec.Error{Message: "uuid invalid"})
	}

	participant, err := api.store.GetParticipant(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return spec.PatchParticipantsParticipantIDConfirmJSON400Response(spec.Error{
				Message: "participant not found",
			})
		}
		api.logger.Error("failed to get participant", zap.Error(err), zap.String("participant_id", participantID))
		return spec.PatchParticipantsParticipantIDConfirmJSON400Response(spec.Error{
			Message: "something went wrong, try again",
		})
	}

	if participant.IsConfirmed {
		return spec.PatchParticipantsParticipantIDConfirmJSON400Response(spec.Error{
			Message: "participant already confirmed",
		})
	}

	if err := api.store.ConfirmParticipant(r.Context(), id); err != nil {
		api.logger.Error("failed to confim participant", zap.Error(err), zap.String("participant_id", participantID))
		return spec.PatchParticipantsParticipantIDConfirmJSON400Response(spec.Error{
			Message: "something went wrong, try again",
		})
	}

	return spec.PatchParticipantsParticipantIDConfirmJSON204Response(nil)
}

// PostTrips Create a new trip
// (POST /trips)
func (api ApiServer) PostTrips(w http.ResponseWriter, r *http.Request) *spec.Response {
	var body spec.CreateTripRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return spec.PostTripsJSON400Response(spec.Error{Message: "invalid JSON"})
	}

	if err := api.validator.Struct(body); err != nil {
		return spec.PostTripsJSON400Response(spec.Error{Message: "invalid input: " + err.Error()})
	}

	tripID, err := api.store.CreateTrip(r.Context(), api.pool, body)
	if err != nil {
		return spec.PostTripsJSON400Response(spec.Error{Message: "failed to create trip, try again"})
	}

	go func() {
		if err := api.mailer.SendConfirmTripEmailToTripOwner(tripID); err != nil {
			api.logger.Error(
				"failed to send email on PostTrips",
				zap.Error(err),
				zap.String("trip_id", tripID.String()),
			)
		}
	}()

	return spec.PostTripsJSON201Response(spec.CreateTripResponse{TripID: tripID.String()})
}

// GetTripsTripID Get a trip details.
// (GET /trips/{tripId})
func (api ApiServer) GetTripsTripID(w http.ResponseWriter, r *http.Request, tripID string) *spec.Response {

	id, err := uuid.Parse(tripID)
	if err != nil {
		return spec.GetTripsTripIDJSON400Response(spec.Error{Message: "uuid invalid"})
	}

	trip, err := api.store.GetTrip(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return spec.GetTripsTripIDJSON400Response(spec.Error{
				Message: "Trip not found",
			})
		}
		api.logger.Error("failed to get trip", zap.Error(err), zap.String("tripID", tripID))
		return spec.GetTripsTripIDJSON400Response(spec.Error{
			Message: "something went wrong, try again",
		})
	}

	responseTrip := spec.GetTripDetailsResponseTripObj{
		ID:          trip.ID.String(),
		Destination: trip.Destination,
		EndsAt:      trip.EndsAt.Time,
		IsConfirmed: trip.IsConfirmed,
		StartsAt:    trip.StartsAt.Time,
	}

	return spec.GetTripsTripIDJSON200Response(spec.GetTripDetailsResponse{Trip: responseTrip})

}

// PutTripsTripID Update a trip.
// (PUT /trips/{tripId})
func (api ApiServer) PutTripsTripID(w http.ResponseWriter, r *http.Request, tripID string) *spec.Response {
	panic("not implemented") // TODO: Implement

}

// GetTripsTripIDActivities Get a trip activities.
// (GET /trips/{tripId}/activities)
func (api ApiServer) GetTripsTripIDActivities(w http.ResponseWriter, r *http.Request, tripID string) *spec.Response {

	id, err := uuid.Parse(tripID)
	if err != nil {
		return spec.GetTripsTripIDActivitiesJSON400Response(spec.Error{Message: "uuid invalid"})
	}

	tripActivities, err := api.store.GetTripActivities(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return spec.GetTripsTripIDActivitiesJSON400Response(spec.Error{
				Message: "no trips found",
			})
		}
		api.logger.Error("failed to get trips", zap.Error(err), zap.String("tripID", tripID))
		return spec.GetTripsTripIDJSON400Response(spec.Error{
			Message: "something went wrong, try again",
		})
	}

	responseActivities := mapActivities(tripActivities)

	response := spec.GetTripActivitiesResponse{
		Activities: responseActivities,
	}

	return spec.GetTripsTripIDActivitiesJSON200Response(response)
}

func mapActivities(activities []pgstore.Activity) []spec.GetTripActivitiesResponseOuterArray {
	activityMap := make(map[time.Time][]spec.GetTripActivitiesResponseInnerArray)
	for _, activity := range activities {
		innerActivity := spec.GetTripActivitiesResponseInnerArray{
			ID:       activity.ID.String(),
			OccursAt: activity.OccursAt.Time,
			Title:    activity.Title,
		}
		activityMap[activity.OccursAt.Time] = append(activityMap[activity.OccursAt.Time], innerActivity)
	}

	var outerActivities []spec.GetTripActivitiesResponseOuterArray
	for date, innerActivities := range activityMap {
		outerActivity := spec.GetTripActivitiesResponseOuterArray{
			Activities: innerActivities,
			Date:       date,
		}
		outerActivities = append(outerActivities, outerActivity)
	}

	return outerActivities
}

// PostTripsTripIDActivities Create a trip activity.
// (POST /trips/{tripId}/activities)
func (api ApiServer) PostTripsTripIDActivities(w http.ResponseWriter, r *http.Request, tripID string) *spec.Response {
	panic("not implemented") // TODO: Implement
}

// GetTripsTripIDConfirm Confirm a trip and send e-mail invitations.
// (GET /trips/{tripId}/confirm)
func (api ApiServer) GetTripsTripIDConfirm(w http.ResponseWriter, r *http.Request, tripID string) *spec.Response {
	panic("not implemented") // TODO: Implement
}

// PostTripsTripIDInvites Invite someone to the trip.
// (POST /trips/{tripId}/invites)
func (api ApiServer) PostTripsTripIDInvites(w http.ResponseWriter, r *http.Request, tripID string) *spec.Response {
	panic("not implemented") // TODO: Implement
}

// GetTripsTripIDLinks Get a trip links.
// (GET /trips/{tripId}/links)
func (api ApiServer) GetTripsTripIDLinks(w http.ResponseWriter, r *http.Request, tripID string) *spec.Response {
	panic("not implemented") // TODO: Implement
}

// PostTripsTripIDLinks Create a trip link.
// (POST /trips/{tripId}/links)
func (api ApiServer) PostTripsTripIDLinks(w http.ResponseWriter, r *http.Request, tripID string) *spec.Response {
	panic("not implemented") // TODO: Implement
}

// GetTripsTripIDParticipants Get a trip participants.
// (GET /trips/{tripId}/participants)
func (api ApiServer) GetTripsTripIDParticipants(w http.ResponseWriter, r *http.Request, tripID string) *spec.Response {
	panic("not implemented") // TODO: Implement
}
