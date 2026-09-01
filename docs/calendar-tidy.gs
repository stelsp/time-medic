/**
 * Calendar tidy - colors, availability and triage, run from your own account.
 *
 * How to use:
 *   1. script.google.com  New project  paste this file over the placeholder
 *   2. Services (+)  Google Calendar API  Add   (gives Calendar.Events.move)
 *   3. Run `preview` first: it changes NOTHING and logs what it would do
 *   4. Happy with the plan? Run `apply`
 *
 * It never deletes anything. Moving an event keeps it - it only changes which
 * calendar owns it, and marking one free only changes whether it blocks your
 * availability for other people.

 * Recurring events are handled as a series: moving or freeing one occurrence
 * applies to the whole series, which is what you want for a daily standup or a
 * twice-a-day pill reminder.
 */

//  what goes where 
// setColor takes a hex string, and these are Google's own palette values.
// The enum was a trap: CalendarApp.Color has no CYAN or MAUVE, and asking for
// one throws "Invalid argument: color" halfway through the run.
var COLORS = {
  work: '#039BE5',      // Peacock
  cats: '#F6BF26',      // Banana
  personal: '#7986CB',  // Lavender
  main: '#616161',      // Graphite
  Birthdays: '#E67C73', // Flamingo
};

// An invitation somebody else owns cannot be moved into another calendar -
// Google refuses to change the organizer's copy. Painting it with the target
// calendar's colour is the next best thing: it reads as work at a glance and
// stays where the invitation landed.
// Painting goes through the event object, not through an id: an invitation
// copy has an id the API refuses to look up ("Not Found"), but the object in
// hand paints fine.
var EVENT_COLOR = {
  work: CalendarApp.EventColor.CYAN,       // Peacock
  cats: CalendarApp.EventColor.YELLOW,     // Banana
  personal: CalendarApp.EventColor.PALE_BLUE, // Lavender
  graphite: CalendarApp.EventColor.GRAY,
};

// paints one event, or its whole series when it repeats
function paint(event, color) {
  if (event.isRecurringEvent()) {
    event.getEventSeries().setColor(color);
  } else {
    event.setColor(color);
  }
}

// Hand overrides, applied last and to every event whatever calendar it sits
// in: some meetings are not what their name suggests. Colour ids are Google's
// event palette - 1 Lavender, 5 Banana, 7 Peacock, 8 Graphite, 11 Tomato.
var OVERRIDES = [
  { patterns: [/achetut/i], color: 'graphite' }, // on the calendar, not on the clock
  { patterns: [/ensomble|ensemble/i], color: 'work' },
  { patterns: [/kuzbass/i], color: 'work' },
];

// an event whose title matches moves to that calendar
// Cyrillic is written as escapes on purpose: this file travels through
// clipboards and editors that mangle encodings, and a broken pattern would
// silently match nothing.
var RULES = [
  { calendar: 'cats', patterns: [/\u0441\u0435\u0440\u0435\u0442\u0438\u0434/i, /\u043f\u0440\u0435\u0434\u043d\u0438\u0437\u043e\u043b/i] },
  { calendar: 'personal', patterns: [/\u0437\u043e\u043b\u043e\u0444\u0442/i, /english/i] },
  { calendar: 'work', patterns: [/w3ds/i, /weekly/i, /ensomble/i, /ensemble/i, /daily/i, /\u0434\u044d\u0439\u043b\u0438/i, /sync/i, /standup/i] },
];

// anyone from these domains makes an event work, whatever it is called
var WORK_DOMAINS = ['time2map.com'];

// events with these titles should not block your colleagues' scheduling
var FREE_PATTERNS = [/\u0441\u0435\u0440\u0435\u0442\u0438\u0434/i, /\u043f\u0440\u0435\u0434\u043d\u0438\u0437\u043e\u043b/i, /\u0437\u043e\u043b\u043e\u0444\u0442/i, /english/i];

var DAYS_BACK = 60;
var DAYS_AHEAD = 120;

//  the run 
// apply comes first on purpose: the editor's function picker defaults to the
// first function in the file, and hunting for the right entry in that dropdown
// is how a "preview" ends up being the thing you keep running by accident.
function apply() { run(false); }
function preview() { run(true); }

function run(dry) {
  var calendars = {};
  CalendarApp.getAllOwnedCalendars().forEach(function (c) { calendars[c.getName()] = c; });

  // 1. colors
  Object.keys(COLORS).forEach(function (name) {
    var cal = calendars[name];
    if (!cal) { Logger.log('no calendar named ' + name + ' - skipped'); return; }
    Logger.log((dry ? 'would set' : 'set') + ' color of ' + name);
    if (!dry) {
      try { cal.setColor(COLORS[name]); }
      catch (e) { Logger.log('  ! color of ' + name + ' refused: ' + e); }
    }
  });

  var from = new Date(Date.now() - DAYS_BACK * 864e5);
  var to = new Date(Date.now() + DAYS_AHEAD * 864e5);

  // 2. collect SERIES, not occurrences. A pill taken twice a day for four
  // months is one decision, not two hundred; moving or freeing it once moves
  // or frees every occurrence with it.
  var series = {}; // key -> {id, title, calendar, count, target, free}
  Object.keys(calendars).forEach(function (name) {
    calendars[name].getEvents(from, to).forEach(function (event) {
      var id = event.getId().split('@')[0];
      var key = name + '|' + id;
      if (series[key]) { series[key].count++; return; }
      var title = event.getTitle();
      series[key] = {
        id: id,
        title: title,
        calendar: name,
        count: 1,
        target: targetFor(title, event),
        ref: event,
        free: matchesAny(title, FREE_PATTERNS) && !event.isAllDayEvent(),
      };
    });
  });

  var moved = 0, freed = 0, kept = 0, failed = 0, painted = 0;
  Object.keys(series).forEach(function (key) {
    var s = series[key];
    var where = ' (' + s.count + (s.count === 1 ? ' event)' : ' occurrences)');

    if (s.target && s.target !== s.calendar && calendars[s.target]) {
      Logger.log((dry ? 'would move' : 'move') + ' "' + s.title + '"  ' +
        s.calendar + ' -> ' + s.target + where);
      var movedThis = true;
      if (!dry) {
        try {
          Calendar.Events.move(calendarId(calendars[s.calendar]), s.id,
            calendarId(calendars[s.target]));
        } catch (e) {
          movedThis = false;
          // somebody else owns this invitation: colour it instead
          var color = EVENT_COLOR[s.target];
          if (color) {
            try {
              paint(s.ref, color);
              Logger.log('  cannot move an invitation - painted it ' + s.target + ' instead');
              painted++;
            } catch (e2) { Logger.log('  ! could not paint: ' + e2); failed++; }
          } else { Logger.log('  ! could not move: ' + e); failed++; }
        }
      }
      if (movedThis) moved++;
      // only after a real move does the event live in the target calendar;
      // in preview it is still where it was, and asking the target for it
      // would fail with Not Found
      if (!dry && movedThis) s.calendar = s.target;
      if (!movedThis) return;
    } else {
      kept++;
    }

    // 3. free/busy: a private block should not read as busy to colleagues.
    // This is `transparency`, which only the advanced service can set.
    if (s.free) {
      try {
        var raw = Calendar.Events.get(calendarId(calendars[s.calendar]), s.id);
        if (raw.transparency !== 'transparent') {
          Logger.log((dry ? 'would free' : 'free') + ' "' + s.title + '"' + where);
          if (!dry) {
            Calendar.Events.patch({ transparency: 'transparent' },
              calendarId(calendars[s.calendar]), s.id);
          }
          freed++;
        }
      } catch (e) { Logger.log('  ! could not free "' + s.title + '": ' + e); failed++; }
    }
  });

  // hand overrides win over everything decided above
  var overridden = 0;
  Object.keys(series).forEach(function (key) {
    var s = series[key];
    for (var i = 0; i < OVERRIDES.length; i++) {
      if (!matchesAny(s.title, OVERRIDES[i].patterns)) continue;
      Logger.log((dry ? 'would paint' : 'paint') + ' "' + s.title + '" as ' + OVERRIDES[i].color);
      if (!dry) {
        try { paint(s.ref, EVENT_COLOR[OVERRIDES[i].color]); }
        catch (e) { Logger.log('  ! could not paint "' + s.title + '": ' + e); failed++; break; }
      }
      overridden++;
      break;
    }
  });

  Logger.log('--- ' + (dry ? 'PREVIEW' : 'APPLIED') + ': ' + overridden + ' overridden, ' +
    Object.keys(series).length + ' series, ' + moved + ' to move, ' +
    freed + ' to mark free, ' + painted + ' painted in place, ' +
    kept + ' already right, ' + failed + ' failed ---');
}

function targetFor(title, event) {
  for (var i = 0; i < RULES.length; i++) {
    if (matchesAny(title, RULES[i].patterns)) return RULES[i].calendar;
  }
  var guests = event.getGuestList();
  for (var g = 0; g < guests.length; g++) {
    var at = guests[g].getEmail().split('@')[1] || '';
    if (WORK_DOMAINS.indexOf(at.toLowerCase()) >= 0) return 'work';
  }
  return null;
}

function matchesAny(title, patterns) {
  return patterns.some(function (p) { return p.test(title); });
}

function calendarId(cal) { return cal.getId(); }
