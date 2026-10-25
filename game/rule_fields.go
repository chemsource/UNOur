package game

// This vocabulary describes engine primitives, not named rule behavior.
// Adding a configuration module never adds an entry keyed by that module's ID.
type ruleContextField struct {
	kind ruleKind
	read func(Context) ruleValue
}
type ruleEffectField struct {
	kind  ruleKind
	read  func(*Effects) ruleValue
	write func(*Effects, ruleValue)
}

var ruleContextFields = map[string]ruleContextField{
	"action": {ruleString, func(c Context) ruleValue { return stringValue(string(c.Action())) }},
	"phase":  {ruleString, func(c Context) ruleValue { return stringValue(string(c.s.phase)) }},
	"chosen_color": {ruleString, func(c Context) ruleValue {
		color := c.command.Color
		if color == KeepColor {
			color = c.ActiveColor()
		}
		return stringValue(string(color))
	}},
	"active_color":     {ruleString, func(c Context) ruleValue { return stringValue(string(c.ActiveColor())) }},
	"pending_draw":     {ruleInt, func(c Context) ruleValue { return intValue(int64(c.PendingDraw())) }},
	"pending_kind":     {ruleString, func(c Context) ruleValue { return stringValue(string(c.PendingKind())) }},
	"alive_players":    {ruleInt, func(c Context) ruleValue { return intValue(int64(c.AlivePlayers())) }},
	"hand_size":        {ruleInt, func(c Context) ruleValue { return intValue(int64(c.HandSize(c.Actor()))) }},
	"has_active_color": {ruleBool, func(c Context) ruleValue { return boolValue(c.HasColor(c.Actor(), c.ActiveColor())) }},
	"drawn_card_id":    {ruleInt, func(c Context) ruleValue { return intValue(int64(c.s.drawnCard)) }},
	"card.id":          {ruleInt, func(c Context) ruleValue { return intValue(int64(c.Card().ID)) }},
	"card.kind":        {ruleString, func(c Context) ruleValue { return stringValue(string(c.Card().Kind)) }},
	"card.color":       {ruleString, func(c Context) ruleValue { return stringValue(string(c.Card().Color)) }},
	"card.number":      {ruleInt, func(c Context) ruleValue { return intValue(int64(c.Card().Number)) }},
	"card.wild":        {ruleBool, func(c Context) ruleValue { return boolValue(c.Card().IsWild()) }},
	"top.exists":       {ruleBool, func(c Context) ruleValue { _, ok := c.TopCard(); return boolValue(ok) }},
	"top.kind":         {ruleString, func(c Context) ruleValue { v, _ := c.TopCard(); return stringValue(string(v.Kind)) }},
	"top.color":        {ruleString, func(c Context) ruleValue { v, _ := c.TopCard(); return stringValue(string(v.Color)) }},
	"top.number":       {ruleInt, func(c Context) ruleValue { v, _ := c.TopCard(); return intValue(int64(v.Number)) }},
	"top.wild":         {ruleBool, func(c Context) ruleValue { v, _ := c.TopCard(); return boolValue(v.IsWild()) }},
}
var ruleEffectFields = map[string]ruleEffectField{
	"allow":                {ruleBool, func(e *Effects) ruleValue { return boolValue(e.Allow) }, func(e *Effects, v ruleValue) { e.Allow = v.boolean }},
	"reverse":              {ruleBool, func(e *Effects) ruleValue { return boolValue(e.Reverse) }, func(e *Effects, v ruleValue) { e.Reverse = v.boolean }},
	"keep_color":           {ruleBool, func(e *Effects) ruleValue { return boolValue(e.KeepColor) }, func(e *Effects, v ruleValue) { e.KeepColor = v.boolean }},
	"allow_challenge":      {ruleBool, func(e *Effects) ruleValue { return boolValue(e.AllowChallenge) }, func(e *Effects, v ruleValue) { e.AllowChallenge = v.boolean }},
	"play_drawn_card":      {ruleBool, func(e *Effects) ruleValue { return boolValue(e.PlayDrawnCard) }, func(e *Effects, v ruleValue) { e.PlayDrawnCard = v.boolean }},
	"play_any_after_draw":  {ruleBool, func(e *Effects) ruleValue { return boolValue(e.PlayAnyAfterDraw) }, func(e *Effects, v ruleValue) { e.PlayAnyAfterDraw = v.boolean }},
	"turn_steps":           {ruleInt, func(e *Effects) ruleValue { return intValue(int64(e.TurnSteps)) }, func(e *Effects, v ruleValue) { e.TurnSteps = int(v.number) }},
	"add_penalty":          {ruleInt, func(e *Effects) ruleValue { return intValue(int64(e.AddPenalty)) }, func(e *Effects, v ruleValue) { e.AddPenalty = int(v.number) }},
	"draw_next":            {ruleInt, func(e *Effects) ruleValue { return intValue(int64(e.DrawNext)) }, func(e *Effects, v ruleValue) { e.DrawNext = int(v.number) }},
	"hand_limit":           {ruleInt, func(e *Effects) ruleValue { return intValue(int64(e.HandLimit)) }, func(e *Effects, v ruleValue) { e.HandLimit = int(v.number) }},
	"challenge_multiplier": {ruleInt, func(e *Effects) ruleValue { return intValue(int64(e.ChallengeMultiplier)) }, func(e *Effects, v ruleValue) { e.ChallengeMultiplier = int(v.number) }},
}
