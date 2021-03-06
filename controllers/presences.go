package controllers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/gin-gonic/gin"
	"github.com/ovh/tat/models"
	"github.com/ovh/tat/utils"
)

// PresencesController contains all methods about presences manipulation
type PresencesController struct{}

type presencesJSON struct {
	Count     int               `json:"count"`
	Presences []models.Presence `json:"presences"`
}

type presenceJSONOut struct {
	Presence models.Presence `json:"presence"`
}

type presenceJSON struct {
	Status string `json:"status" binding:"required"`
	Topic  string
}

func (*PresencesController) buildCriteria(ctx *gin.Context) *models.PresenceCriteria {
	c := models.PresenceCriteria{}
	skip, e := strconv.Atoi(ctx.DefaultQuery("skip", "0"))
	if e != nil {
		skip = 0
	}
	c.Skip = skip
	limit, e2 := strconv.Atoi(ctx.DefaultQuery("limit", "100"))
	if e2 != nil {
		limit = 10
	}
	c.Limit = limit
	c.IDPresence = ctx.Query("idPresence")
	c.Status = ctx.Query("status")
	c.Username = ctx.Query("username")
	c.DateMinPresence = ctx.Query("dateMinPresence")
	c.DateMaxPresence = ctx.Query("dateMaxPresence")
	return &c
}

// List list presences with given criterias
func (m *PresencesController) List(ctx *gin.Context) {
	topicIn, err := GetParam(ctx, "topic")
	if err != nil {
		return
	}
	criteria := m.buildCriteria(ctx)
	criteria.Topic = topicIn

	m.listWithCriteria(ctx, criteria)
}

func (m *PresencesController) listWithCriteria(ctx *gin.Context, criteria *models.PresenceCriteria) {
	user, e := m.preCheckUser(ctx)
	if e != nil {
		return
	}
	var topic = models.Topic{}
	err := topic.FindByTopic(criteria.Topic, true)
	if err != nil {
		ctx.AbortWithError(http.StatusBadRequest, errors.New("topic "+criteria.Topic+" does not exist"))
		return
	}

	isReadAccess := topic.IsUserReadAccess(user)
	if !isReadAccess {
		ctx.AbortWithError(http.StatusForbidden, errors.New("No Read Access to this topic."))
		return
	}
	// add / if search on topic
	// as topic is in path, it can't start with a /
	if criteria.Topic != "" && string(criteria.Topic[0]) != "/" {
		criteria.Topic = "/" + criteria.Topic
	}

	topicDM := "/Private/" + utils.GetCtxUsername(ctx) + "/DM/"
	if strings.HasPrefix(criteria.Topic, topicDM) {
		part := strings.Split(criteria.Topic, "/")
		if len(part) != 5 {
			log.Errorf("wrong topic name for DM")
			ctx.AbortWithError(http.StatusInternalServerError, errors.New("Wrong topic name for DM:"+criteria.Topic))
			return
		}
		topicInverse := "/Private/" + part[4] + "/DM/" + utils.GetCtxUsername(ctx)
		criteria.Topic = criteria.Topic + "," + topicInverse
	}

	count, presences, err := models.ListPresences(criteria)
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, err)
		return
	}
	out := &presencesJSON{
		Count:     count,
		Presences: presences,
	}
	ctx.JSON(http.StatusOK, out)
}

func (m *PresencesController) preCheckTopic(ctx *gin.Context) (presenceJSON, models.Topic, error) {
	var topic = models.Topic{}
	var presenceIn presenceJSON
	ctx.Bind(&presenceIn)

	topicIn, err := GetParam(ctx, "topic")
	if err != nil {
		return presenceIn, topic, err
	}
	presenceIn.Topic = topicIn

	err = topic.FindByTopic(presenceIn.Topic, true)
	if err != nil {
		e := errors.New("Topic " + presenceIn.Topic + " does not exist")
		ctx.AbortWithError(http.StatusInternalServerError, e)
		return presenceIn, topic, e
	}
	return presenceIn, topic, nil
}

func (*PresencesController) preCheckUser(ctx *gin.Context) (models.User, error) {
	var user = models.User{}
	err := user.FindByUsername(utils.GetCtxUsername(ctx))
	if err != nil {
		e := errors.New("Error while fetching user.")
		ctx.AbortWithError(http.StatusInternalServerError, e)
		return user, e
	}
	return user, nil
}

func (m *PresencesController) create(ctx *gin.Context) {

	presenceIn, topic, e := m.preCheckTopic(ctx)
	if e != nil {
		return
	}

	user, e := m.preCheckUser(ctx)
	if e != nil {
		return
	}

	isReadAccess := topic.IsUserReadAccess(user)
	if !isReadAccess {
		e := errors.New("No Read Access to topic " + presenceIn.Topic + " for user " + user.Username)
		ctx.AbortWithError(http.StatusForbidden, e)
		ctx.JSON(http.StatusForbidden, e)
		return
	}

	var presence = models.Presence{}
	err := presence.Upsert(user, topic, presenceIn.Status)
	if err != nil {
		log.Errorf("Error while InsertPresence %s", err)
		ctx.AbortWithError(http.StatusInternalServerError, err)
		ctx.JSON(http.StatusInternalServerError, err)
		return
	}

	go models.WSPresence(&models.WSPresenceJSON{Action: "create", Presence: presence})

	//out := &presenceJSONOut{Presence: presence}
	//ctx.JSON(http.StatusCreated, nil)
}

// CreateAndGet creates a presence and get presences on current topic
func (m *PresencesController) CreateAndGet(ctx *gin.Context) {
	m.create(ctx)
	if ctx.IsAborted() {
		return
	}

	fiften := strconv.FormatInt(time.Now().Unix()-15, 10)

	topicIn, _ := GetParam(ctx, "topic") // no error possible here
	criteria := &models.PresenceCriteria{
		Skip:            0,
		Limit:           1000,
		Topic:           topicIn,
		DateMinPresence: fiften,
	}

	m.listWithCriteria(ctx, criteria)
}
