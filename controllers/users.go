package controllers

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/gin-gonic/gin"
	"github.com/ovh/tat/models"
	"github.com/ovh/tat/utils"
	"github.com/spf13/viper"
)

// UsersController contains all methods about users manipulation
type UsersController struct{}

type usersJSON struct {
	Count int           `json:"count"`
	Users []models.User `json:"users"`
}

func (*UsersController) buildCriteria(ctx *gin.Context) *models.UserCriteria {
	c := models.UserCriteria{}
	skip, e := strconv.Atoi(ctx.DefaultQuery("skip", "0"))
	if e != nil {
		skip = 0
	}
	c.Skip = skip
	limit, e2 := strconv.Atoi(ctx.DefaultQuery("limit", "100"))
	if e2 != nil {
		limit = 100
	}
	withGroups, e := strconv.ParseBool(ctx.DefaultQuery("withGroups", "false"))
	if e != nil {
		withGroups = false
	}
	c.Limit = limit
	c.WithGroups = withGroups
	c.IDUser = ctx.Query("idUser")

	c.Username = ctx.Query("username")
	c.Fullname = ctx.Query("fullname")
	c.DateMinCreation = ctx.Query("dateMinCreation")
	c.DateMaxCreation = ctx.Query("dateMaxCreation")
	return &c
}

// List list all users matching Criteria
func (u *UsersController) List(ctx *gin.Context) {
	criteria := u.buildCriteria(ctx)
	count, users, err := models.ListUsers(criteria, utils.IsTatAdmin(ctx))
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, err)
		return
	}
	out := &usersJSON{
		Count: count,
		Users: users,
	}
	ctx.JSON(http.StatusOK, out)
}

type userCreateJSON struct {
	Username string `json:"username"  binding:"required"`
	Fullname string `json:"fullname"  binding:"required"`
	Email    string `json:"email"     binding:"required"`
	Callback string `json:"callback"`
}

// Create a new user, record Username, Fullname and Email
// A mail is sent to ask user for validation
func (u *UsersController) Create(ctx *gin.Context) {
	var userJSON userCreateJSON
	ctx.Bind(&userJSON)
	var userIn models.User
	userIn.Username = u.computeUsername(userJSON)
	userIn.Fullname = strings.TrimSpace(userJSON.Fullname)
	userIn.Email = strings.TrimSpace(userJSON.Email)
	callback := strings.TrimSpace(userJSON.Callback)

	if len(userIn.Username) < 3 || len(userIn.Fullname) < 3 || len(userIn.Email) < 7 {
		err := fmt.Errorf("Invalid username (%s) or fullname (%s) or email (%s)", userIn.Username, userIn.Fullname, userIn.Email)
		AbortWithReturnError(ctx, http.StatusInternalServerError, err)
		return
	}

	err := u.checkAllowedDomains(userJSON)
	if err != nil {
		ctx.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	if models.IsEmailExists(userJSON.Email) || models.IsUsernameExists(userJSON.Username) || models.IsFullnameExists(userJSON.Fullname) {
		e := fmt.Errorf("Please check your username, email or fullname. If you are already registered, please reset your password")
		AbortWithReturnError(ctx, http.StatusBadRequest, e)
		return
	}

	tokenVerify, err := userIn.Insert()
	if err != nil {
		log.Errorf("Error while InsertUser %s", err)
		ctx.AbortWithError(http.StatusInternalServerError, err)
		return
	}

	go utils.SendVerifyEmail(userIn.Username, userIn.Email, tokenVerify, callback)
	go models.WSUser(&models.WSUserJSON{Action: "create", Username: userIn.Username})

	info := ""
	if viper.GetBool("username_from_email") {
		info = fmt.Sprintf(" Note that configuration of Tat forced your username to %s", userIn.Username)
	}
	ctx.JSON(http.StatusCreated, gin.H{"info": fmt.Sprintf("please check your mail to validate your account.%s", info)})
}

func (u *UsersController) checkAllowedDomains(userJSON userCreateJSON) error {
	if viper.GetString("allowed_domains") != "" {
		allowedDomains := strings.Split(viper.GetString("allowed_domains"), ",")
		for _, domain := range allowedDomains {
			if strings.HasSuffix(userJSON.Email, "@"+domain) {
				return nil
			}
		}
		return fmt.Errorf("Your email domain is not allowed on this instance of Tat.")
	}
	return nil
}

// computeUsername returns first.lastname for first.lastname@domainA.com if
// parameter username_from_email=true on tat binary
func (u *UsersController) computeUsername(userJSON userCreateJSON) string {
	if viper.GetBool("username_from_email") {
		i := strings.Index(userJSON.Email, "@")
		if i > 0 {
			return userJSON.Email[0:i]
		}
	}
	return userJSON.Username
}

// Verify is called by user, after receive email to validate his account
func (u *UsersController) Verify(ctx *gin.Context) {
	var user = &models.User{}
	username, err := GetParam(ctx, "username")
	if err != nil {
		return
	}
	tokenVerify, err := GetParam(ctx, "tokenVerify")
	if err != nil {
		return
	}
	if username != "" && tokenVerify != "" {
		isNewUser, password, err := user.Verify(username, tokenVerify)
		if err != nil {
			e := fmt.Sprintf("Error on verify token for username %s", username)
			log.Errorf("%s %s", e, err.Error())
			ctx.JSON(http.StatusInternalServerError, gin.H{"info": e})
		} else {
			ctx.JSON(http.StatusOK, gin.H{
				"message":  "Verification successfull",
				"username": username,
				"password": password,
				"url":      fmt.Sprintf("%s://%s:%s%s", viper.GetString("exposed_scheme"), viper.GetString("exposed_host"), viper.GetString("exposed_port"), viper.GetString("exposed_path")),
			})

			if isNewUser {
				go models.WSUser(&models.WSUserJSON{Action: "verify", Username: username})
			}
		}
	} else {
		ctx.JSON(http.StatusBadRequest, gin.H{"info": fmt.Sprintf("username %s or token empty", username)})
	}
}

type userResetJSON struct {
	Username string `json:"username"  binding:"required"`
	Email    string `json:"email"     binding:"required"`
	Callback string `json:"callback"`
}

// Reset send a mail asking user to confirm reset password
func (u *UsersController) Reset(ctx *gin.Context) {
	var userJSON userResetJSON
	ctx.Bind(&userJSON)
	var userIn models.User
	userIn.Username = strings.TrimSpace(userJSON.Username)
	userIn.Email = strings.TrimSpace(userJSON.Email)
	callback := strings.TrimSpace(userJSON.Callback)

	if len(userIn.Username) < 3 || len(userIn.Email) < 7 {
		err := fmt.Errorf("Invalid username (%s) or email (%s)", userIn.Username, userIn.Email)
		AbortWithReturnError(ctx, http.StatusInternalServerError, err)
		return
	}

	tokenVerify, err := userIn.AskReset()
	if err != nil {
		log.Errorf("Error while AskReset %s", err)
		ctx.AbortWithError(http.StatusInternalServerError, err)
		return
	}

	go utils.SendAskResetEmail(userIn.Username, userIn.Email, tokenVerify, callback)
	ctx.JSON(http.StatusCreated, gin.H{"info": "please check your mail to validate your account"})
}

type userJSON struct {
	User *models.User `json:"user"`
}

// Me retrieves all information about me (exception information about Authentication)
func (*UsersController) Me(ctx *gin.Context) {
	var user = models.User{}
	err := user.FindByUsername(utils.GetCtxUsername(ctx))
	if err != nil {
		AbortWithReturnError(ctx, http.StatusInternalServerError, errors.New("Error while fetching user"))
		return
	}
	out := &userJSON{User: &user}
	ctx.JSON(http.StatusOK, out)
}

type contactsJSON struct {
	Contacts               []models.Contact   `json:"contacts"`
	CountContactsPresences int                `json:"countContactsPresences"`
	ContactsPresences      *[]models.Presence `json:"contactsPresence"`
}

// Contacts retrieves contacts presences since n seconds
func (*UsersController) Contacts(ctx *gin.Context) {
	sinceSeconds, err := GetParam(ctx, "sinceSeconds")
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Error while getting seconds parameter"})
		return
	}
	seconds, err := strconv.ParseInt(sinceSeconds, 10, 64)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Invalid since parameter : must be an interger"})
		return
	}

	var user = models.User{}
	err = user.FindByUsername(utils.GetCtxUsername(ctx))
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, errors.New("Error while fetching user"))
		return
	}
	criteria := models.PresenceCriteria{}
	for _, contact := range user.Contacts {
		criteria.Username = criteria.Username + "," + contact.Username
	}
	criteria.DateMinPresence = strconv.FormatInt(time.Now().Unix()-seconds, 10)
	count, presences, _ := models.ListPresences(&criteria)

	out := &contactsJSON{
		Contacts:               user.Contacts,
		CountContactsPresences: count,
		ContactsPresences:      &presences,
	}
	ctx.JSON(http.StatusOK, out)
}

// AddContact add a contact to user
func (*UsersController) AddContact(ctx *gin.Context) {
	contactIn, err := GetParam(ctx, "username")
	if err != nil {
		return
	}
	user, err := PreCheckUser(ctx)
	if err != nil {
		return
	}

	var contact = models.User{}
	err = contact.FindByUsername(contactIn)
	if err != nil {
		AbortWithReturnError(ctx, http.StatusBadRequest, fmt.Errorf("user with username %s does not exist", contactIn))
		return
	}

	err = user.AddContact(contact.Username, contact.Fullname)
	if err != nil {
		AbortWithReturnError(ctx, http.StatusInternalServerError, fmt.Errorf("Error while add contact %s to user:%s", contact.Username, user.Username))
		return
	}
	ctx.JSON(http.StatusCreated, "")
}

// RemoveContact removes a contact from user
func (*UsersController) RemoveContact(ctx *gin.Context) {
	contactIn, err := GetParam(ctx, "username")
	if err != nil {
		return
	}
	user, err := PreCheckUser(ctx)
	if err != nil {
		return
	}

	err = user.RemoveContact(contactIn)
	if err != nil {
		AbortWithReturnError(ctx, http.StatusInternalServerError, fmt.Errorf("Error while remove contact %s to user:%s", contactIn, user.Username))
		return
	}
	ctx.JSON(http.StatusOK, "")
}

// AddFavoriteTopic add a favorite topic to user
func (*UsersController) AddFavoriteTopic(ctx *gin.Context) {
	topicIn, err := GetParam(ctx, "topic")
	if err != nil {
		return
	}
	user, err := PreCheckUser(ctx)
	if err != nil {
		return
	}

	var topic = models.Topic{}
	err = topic.FindByTopic(topicIn, true)
	if err != nil {
		AbortWithReturnError(ctx, http.StatusBadRequest, errors.New("topic "+topicIn+" does not exist"))
		return
	}

	isReadAccess := topic.IsUserReadAccess(user)
	if !isReadAccess {
		AbortWithReturnError(ctx, http.StatusForbidden, errors.New("No Read Access to this topic"))
		return
	}

	err = user.AddFavoriteTopic(topic.Topic)
	if err != nil {
		AbortWithReturnError(ctx, http.StatusInternalServerError, fmt.Errorf("Error while add favorite topic to user:%s", user.Username))
		return
	}
	ctx.JSON(http.StatusCreated, gin.H{"info": fmt.Sprintf("Topic %s added to favorites", topic.Topic)})
}

// RemoveFavoriteTopic removes favorite topic from user
func (*UsersController) RemoveFavoriteTopic(ctx *gin.Context) {
	topicIn, err := GetParam(ctx, "topic")
	if err != nil {
		return
	}
	user, err := PreCheckUser(ctx)
	if err != nil {
		return
	}

	err = user.RemoveFavoriteTopic(topicIn)
	if err != nil {
		e := fmt.Errorf("Error while remove favorite topic %s to user:%s err:%s", topicIn, user.Username, err.Error())
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": e.Error()})
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"info": fmt.Sprintf("Topic %s removed from favorites", topicIn)})
}

// EnableNotificationsTopic enable notication on one topic
func (*UsersController) EnableNotificationsTopic(ctx *gin.Context) {
	topicIn, err := GetParam(ctx, "topic")
	if err != nil {
		return
	}
	user, err := PreCheckUser(ctx)
	if err != nil {
		return
	}

	var topic = models.Topic{}
	err = topic.FindByTopic(topicIn, true)
	if err != nil {
		AbortWithReturnError(ctx, http.StatusBadRequest, errors.New("topic "+topicIn+" does not exist"))
		return
	}

	isReadAccess := topic.IsUserReadAccess(user)
	if !isReadAccess {
		AbortWithReturnError(ctx, http.StatusForbidden, errors.New("No Read Access to this topic"))
		return
	}

	err = user.EnableNotificationsTopic(topic.Topic)
	if err != nil {
		AbortWithReturnError(ctx, http.StatusInternalServerError, fmt.Errorf("Error while enable notication on topic %s to user:%s", topic.Topic, user.Username))
		return
	}
	ctx.JSON(http.StatusCreated, gin.H{"info": fmt.Sprintf("Notications enabled on Topic %s", topic.Topic)})
}

// DisableNotificationsTopic disable notifications on one topic
func (*UsersController) DisableNotificationsTopic(ctx *gin.Context) {
	topicIn, err := GetParam(ctx, "topic")
	if err != nil {
		return
	}
	user, err := PreCheckUser(ctx)
	if err != nil {
		return
	}

	err = user.DisableNotificationsTopic(topicIn)
	if err != nil {
		e := fmt.Errorf("Error while disable notications on topic %s to user:%s err:%s", topicIn, user.Username, err.Error())
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": e.Error()})
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"info": fmt.Sprintf("Notications disabled on topic %s", topicIn)})
}

// AddFavoriteTag add a favorite tag to user
func (*UsersController) AddFavoriteTag(ctx *gin.Context) {
	tagIn, err := GetParam(ctx, "tag")
	if err != nil {
		return
	}
	user, err := PreCheckUser(ctx)
	if err != nil {
		return
	}

	err = user.AddFavoriteTag(tagIn)
	if err != nil {
		AbortWithReturnError(ctx, http.StatusInternalServerError, fmt.Errorf("Error while add favorite tag to user:%s", user.Username))
		return
	}
	ctx.JSON(http.StatusCreated, "")
}

// RemoveFavoriteTag removes a favorite tag from user
func (*UsersController) RemoveFavoriteTag(ctx *gin.Context) {
	tagIn, err := GetParam(ctx, "tag")
	if err != nil {
		return
	}
	user, err := PreCheckUser(ctx)
	if err != nil {
		return
	}

	err = user.RemoveFavoriteTag(tagIn)
	if err != nil {
		AbortWithReturnError(ctx, http.StatusInternalServerError, fmt.Errorf("Error while remove favorite tag to user:%s", user.Username))
		return
	}
	ctx.JSON(http.StatusOK, "")
}

type convertUserJSON struct {
	Username              string `json:"username"  binding:"required"`
	CanWriteNotifications bool   `json:"canWriteNotifications"  binding:"required"`
}

// Convert a "normal" user to a "system" user
func (*UsersController) Convert(ctx *gin.Context) {
	var convertJSON convertUserJSON
	ctx.Bind(&convertJSON)

	if !strings.HasPrefix(convertJSON.Username, "tat.system") {
		AbortWithReturnError(ctx, http.StatusBadRequest, fmt.Errorf("Username does not begin with tat.system (%s), it's not possible to convert this user", convertJSON.Username))
		return
	}

	var userToConvert = models.User{}
	err := userToConvert.FindByUsername(convertJSON.Username)
	if err != nil {
		AbortWithReturnError(ctx, http.StatusBadRequest, fmt.Errorf("user with username %s does not exist", convertJSON.Username))
		return
	}

	if userToConvert.IsSystem {
		AbortWithReturnError(ctx, http.StatusBadRequest, fmt.Errorf("user with username %s is already a system user", convertJSON.Username))
		return
	}

	newPassword, err := userToConvert.ConvertToSystem(utils.GetCtxUsername(ctx), convertJSON.CanWriteNotifications)
	if err != nil {
		AbortWithReturnError(ctx, http.StatusBadRequest, fmt.Errorf("Convert %s to system user failed", convertJSON.Username))
		return
	}

	ctx.JSON(http.StatusOK, gin.H{
		"message":  "Verification successfull",
		"username": userToConvert.Username,
		"password": newPassword,
		"url":      fmt.Sprintf("%s://%s:%s%s", viper.GetString("exposed_scheme"), viper.GetString("exposed_host"), viper.GetString("exposed_port"), viper.GetString("exposed_path")),
	})
}

type resetSystemUserJSON struct {
	Username string `json:"username"  binding:"required"`
}

// ResetSystemUser reset password for a system user
func (*UsersController) ResetSystemUser(ctx *gin.Context) {
	var systemUserJSON resetSystemUserJSON
	ctx.Bind(&systemUserJSON)

	if !strings.HasPrefix(systemUserJSON.Username, "tat.system") {
		AbortWithReturnError(ctx, http.StatusBadRequest, fmt.Errorf("Username does not begin with tat.system (%s), it's not possible to reset password for this user", systemUserJSON.Username))
		return
	}

	var systemUserToReset = models.User{}
	err := systemUserToReset.FindByUsername(systemUserJSON.Username)
	if err != nil {
		AbortWithReturnError(ctx, http.StatusBadRequest, fmt.Errorf("user with username %s does not exist", systemUserJSON.Username))
		return
	}

	if !systemUserToReset.IsSystem {
		AbortWithReturnError(ctx, http.StatusBadRequest, fmt.Errorf("user with username %s is not a system user", systemUserJSON.Username))
		return
	}

	newPassword, err := systemUserToReset.ResetSystemUserPassword()
	if err != nil {
		AbortWithReturnError(ctx, http.StatusBadRequest, fmt.Errorf("Reset password for %s (system user) failed", systemUserJSON.Username))
		return
	}

	ctx.JSON(http.StatusOK, gin.H{
		"message":  "Reset password successfull",
		"username": systemUserToReset.Username,
		"password": newPassword,
		"url":      fmt.Sprintf("%s://%s:%s%s", viper.GetString("exposed_scheme"), viper.GetString("exposed_host"), viper.GetString("exposed_port"), viper.GetString("exposed_path")),
	})
}

// SetAdmin a "normal" user to an admin user
func (*UsersController) SetAdmin(ctx *gin.Context) {
	var convertJSON convertUserJSON
	ctx.Bind(&convertJSON)

	var userToGrant = models.User{}
	err := userToGrant.FindByUsername(convertJSON.Username)
	if err != nil {
		AbortWithReturnError(ctx, http.StatusBadRequest, fmt.Errorf("user with username %s does not exist", convertJSON.Username))
		return
	}

	if userToGrant.IsAdmin {
		AbortWithReturnError(ctx, http.StatusBadRequest, fmt.Errorf("user with username %s is already an admin user", convertJSON.Username))
		return
	}

	err = userToGrant.ConvertToAdmin(utils.GetCtxUsername(ctx))
	if err != nil {
		AbortWithReturnError(ctx, http.StatusBadRequest, fmt.Errorf("Convert %s to admin user failed", convertJSON.Username))
		return
	}

	ctx.JSON(http.StatusCreated, "")
}

type usernameUserJSON struct {
	Username string `json:"username"  binding:"required"`
}

// Archive a user
func (*UsersController) Archive(ctx *gin.Context) {
	var archiveJSON usernameUserJSON
	ctx.Bind(&archiveJSON)

	var userToArchive = models.User{}
	err := userToArchive.FindByUsername(archiveJSON.Username)
	if err != nil {
		AbortWithReturnError(ctx, http.StatusBadRequest, fmt.Errorf("user with username %s does not exist", archiveJSON.Username))
		return
	}

	if userToArchive.IsArchived {
		AbortWithReturnError(ctx, http.StatusBadRequest, fmt.Errorf("user with username %s is already archived", archiveJSON.Username))
		return
	}

	err = userToArchive.Archive(utils.GetCtxUsername(ctx))
	if err != nil {
		AbortWithReturnError(ctx, http.StatusBadRequest, fmt.Errorf("archive user %s failed", archiveJSON.Username))
		return
	}

	ctx.JSON(http.StatusCreated, "")
}

type renameUserJSON struct {
	Username    string `json:"username"  binding:"required"`
	NewUsername string `json:"newUsername"  binding:"required"`
}

// Rename a username of one user
func (*UsersController) Rename(ctx *gin.Context) {
	var renameJSON renameUserJSON
	ctx.Bind(&renameJSON)

	var userToRename = models.User{}
	err := userToRename.FindByUsername(renameJSON.Username)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": fmt.Errorf("user with username %s does not exist", renameJSON.Username)})
		return
	}

	err = userToRename.Rename(renameJSON.NewUsername)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": fmt.Errorf("Rename %s user to %s failed", renameJSON.Username, renameJSON.NewUsername)})
		return
	}

	ctx.JSON(http.StatusCreated, "")
}

type updateUserJSON struct {
	Username    string `json:"username"  binding:"required"`
	NewFullname string `json:"newFullname" binding:"required"`
	NewEmail    string `json:"newEmail" binding:"required"`
}

// Update changes fullname and email
func (*UsersController) Update(ctx *gin.Context) {
	var updateJSON updateUserJSON
	ctx.Bind(&updateJSON)

	var userToUpdate = models.User{}
	err := userToUpdate.FindByUsername(updateJSON.Username)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": fmt.Errorf("user with username %s does not exist", updateJSON.Username)})
		return
	}

	if strings.TrimSpace(updateJSON.NewFullname) == "" || strings.TrimSpace(updateJSON.NewEmail) == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": fmt.Errorf("Invalid Fullname %s or Email %s", updateJSON.NewFullname, updateJSON.NewEmail)})
		return
	}

	err = userToUpdate.Update(strings.TrimSpace(updateJSON.NewFullname), strings.TrimSpace(updateJSON.NewEmail))
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Update %s user to fullname %s and email %s failed : %s", updateJSON.Username, updateJSON.NewFullname, updateJSON.NewEmail, err.Error())})
		return
	}

	ctx.JSON(http.StatusCreated, "")
}

type checkTopicsUserJSON struct {
	Username         string `json:"username"  binding:"required"`
	FixPrivateTopics bool   `json:"fixPrivateTopics"  binding:"required"`
	FixDefaultGroup  bool   `json:"fixDefaultGroup"  binding:"required"`
}

// Check if user have his Private topics
// /Private/username, /Private/username/Tasks, /Private/username/Bookmarks
func (u *UsersController) Check(ctx *gin.Context) {

	var userJSON checkTopicsUserJSON
	ctx.Bind(&userJSON)

	var userToCheck = models.User{}
	err := userToCheck.FindByUsername(userJSON.Username)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": fmt.Errorf("user with username %s does not exist", userJSON.Username)})
		return
	}

	topicsInfo := u.checkTopics(userJSON.FixPrivateTopics, userToCheck)
	defaultGroupInfo := u.checkDefaultGroup(userJSON.FixDefaultGroup, userToCheck)

	ctx.JSON(http.StatusCreated, gin.H{"topics": topicsInfo, "defaultGroup": defaultGroupInfo})
}

func (*UsersController) checkDefaultGroup(fixDefaultGroup bool, userToCheck models.User) string {
	defaultGroupInfo := ""

	userGroups, err := userToCheck.GetGroupsOnlyName()
	if err != nil {
		return "Error while fetching user groups"
	}

	find := false
	for _, g := range userGroups {
		if g == viper.GetString("default_group") {
			find = true
			defaultGroupInfo = fmt.Sprintf("user in %s OK", viper.GetString("default_group"))
			break
		}
	}
	if !find {
		if fixDefaultGroup {
			err = userToCheck.AddDefaultGroup()
			if err != nil {
				return err.Error()
			}
			defaultGroupInfo = fmt.Sprintf("user added in default group %s", viper.GetString("default_group"))
		} else {
			defaultGroupInfo = fmt.Sprintf("user in default group %s KO", viper.GetString("default_group"))
		}
	}
	return defaultGroupInfo
}

func (*UsersController) checkTopics(fixTopics bool, userToCheck models.User) string {
	topicsInfo := ""
	topicNames := [...]string{"", "Tasks", "Bookmarks", "Notifications"}
	for _, shortName := range topicNames {
		topicName := fmt.Sprintf("/Private/%s", userToCheck.Username)
		if shortName != "" {
			topicName = fmt.Sprintf("%s/%s", topicName, shortName)
		}
		topic := &models.Topic{}
		errfinding := topic.FindByTopic(topicName, false)
		if errfinding != nil {
			topicsInfo = fmt.Sprintf("%s %s KO : not exist; ", topicsInfo, topicName)
			if fixTopics {
				err := userToCheck.CreatePrivateTopic(shortName)
				if err != nil {
					topicsInfo = fmt.Sprintf("%s Error while creating %s; ", topicsInfo, topicName)
				} else {
					topicsInfo = fmt.Sprintf("%s %s created; ", topicsInfo, topicName)
				}
			}
		} else {
			topicsInfo = fmt.Sprintf("%s %s OK; ", topicsInfo, topicName)
		}
	}
	return topicsInfo
}
