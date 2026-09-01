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
var COLORS = {
  work: CalendarApp.Color.CYAN,      // Peacock
  cats: CalendarApp.Color.YELLOW,    // Banana
  personal: CalendarApp.Color.MAUVE, // Lavender
  main: CalendarApp.Color.GRAY,      // Graphite
  Birthdays: CalendarApp.Color.PINK, // Flamingo
};

// an event whose title matches moves to that calendar
// Cyrillic is written as escapes on purpose: this file travels through
// clipboards and editors that mangle encodings, and a broken pattern would
// silently match nothing.
var RULES = [
  { calendar: 'cats', patterns: [/\u0441\u0435\u0440\u0435\u0442\u0438\u0434/i, /\u043f\u0440\u0435\u0434\u043d\u0438\u0437\u043e\u043b/i] },
  { calendar: 'personal', patterns: [/\u0437\u043e\u043b\u043e\u0444\u0442/i, /english/i] },
  { calendar: 'work', patterns: [/w3ds/i, /weekly/i, /ensemble/i, /daily/i, /\u0434\u044d\u0439\u043b\u0438/i, /sync/i, /standup/i] },
];

// anyone from these domains makes an event work, whatever it is called
var WORK_DOMAINS = ['time2map.com'];

// events with these titles should not block your colleagues' scheduling
var FREE_PATTERNS = [/\u0441\u0435\u0440\u0435\u0442\u0438\u0434/i, /\u043f\u0440\u0435\u0434\u043d\u0438\u0437\u043e\u043b/i, /\u0437\u043e\u043b\u043e\u0444\u0442/i, /english/i];

var DAYS_BACK = 60;
var DAYS_AHEAD = 120;

//  the run 
function preview() { run(true); }
function apply() { run(false); }

function run(dry) {
  var calendars = {};
  CalendarApp.getAllOwnedCalendars().forEach(function (c) { calendars[c.getName()] = c; });

  // 1. colors
  Object.keys(COLORS).forEach(function (name) {
    var cal = calendars[name];
    if (!cal) { Logger.log('no calendar named ' + name + ' - skipped'); return; }
    Logger.log((dry ? 'would set' : 'set') + ' color of ' + name);
    if (!dry) cal.setColor(COLORS[name]);
  });

  var from = new Date(Date.now() - DAYS_BACK * 864e5);
  var to = new Date(Date.now() + DAYS_AHEAD * 864e5);
  var moved = 0, freed = 0, seen = 0;

  Object.keys(calendars).forEach(function (name) {
    calendars[name].getEvents(from, to).forEach(function (event) {
      seen++;
      var title = event.getTitle();
      var target = targetFor(title, event);

      // 2. triage: an event in the wrong calendar moves to the right one
      if (target && target !== name && calendars[target]) {
        Logger.log((dry ? 'would move' : 'move') + ' "' + title + '"  ' + name + '  ' + target);
        if (!dry) {
          Calendar.Events.move(calendarId(calendars[name]), event.getId().split('@')[0],
            calendarId(calendars[target]));
        }
        moved++;
        return; // the moved event keeps its own availability
      }

      // 3. availability: a private block should not read as busy to colleagues.
      // free/busy is `transparency`, which only the advanced service can set -
      // CalendarApp's visibility is a different thing (who may see the event).
      if (matchesAny(title, FREE_PATTERNS) && !event.isAllDayEvent()) {
        try {
          var id = event.getId().split('@')[0];
          var raw = Calendar.Events.get(calendarId(calendars[name]), id);
          if (raw.transparency !== 'transparent') {
            Logger.log((dry ? 'would free' : 'free') + ' "' + title + '"');
            if (!dry) {
              Calendar.Events.patch({ transparency: 'transparent' },
                calendarId(calendars[name]), id);
            }
            freed++;
          }
        } catch (e) { Logger.log('cannot free "' + title + '": ' + e); }
      }
    });
  });

  Logger.log('--- ' + (dry ? 'PREVIEW' : 'APPLIED') + ': ' + seen + ' events seen, ' +
    moved + ' to move, ' + freed + ' to mark private ---');
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
