/**
 * Calendar tidy — colors, availability and triage, run from your own account.
 *
 * How to use:
 *   1. script.google.com → New project → paste this file over the placeholder
 *   2. Services (+) → Google Calendar API → Add   (gives Calendar.Events.move)
 *   3. Run `preview` first: it changes NOTHING and logs what it would do
 *   4. Happy with the plan? Run `apply`
 *
 * It never deletes anything. Moving an event keeps it — it only changes which
 * calendar owns it.
 */

// ── what goes where ─────────────────────────────────────────────────────────
var COLORS = {
  work: CalendarApp.Color.CYAN,      // Peacock
  cats: CalendarApp.Color.YELLOW,    // Banana
  personal: CalendarApp.Color.MAUVE, // Lavender
  main: CalendarApp.Color.GRAY,      // Graphite
  Birthdays: CalendarApp.Color.PINK, // Flamingo
};

// an event whose title matches moves to that calendar
var RULES = [
  { calendar: 'cats', patterns: [/серетид/i, /преднизол/i] },
  { calendar: 'personal', patterns: [/золофт/i, /english/i] },
  { calendar: 'work', patterns: [/w3ds/i, /weekly/i, /ensemble/i, /daily/i, /дэйли/i, /sync/i, /standup/i] },
];

// anyone from these domains makes an event work, whatever it is called
var WORK_DOMAINS = ['time2map.com'];

// events with these titles should not block your colleagues' scheduling
var FREE_PATTERNS = [/серетид/i, /преднизол/i, /золофт/i, /english/i];

var DAYS_BACK = 60;
var DAYS_AHEAD = 120;

// ── the run ─────────────────────────────────────────────────────────────────
function preview() { run(true); }
function apply() { run(false); }

function run(dry) {
  var calendars = {};
  CalendarApp.getAllOwnedCalendars().forEach(function (c) { calendars[c.getName()] = c; });

  // 1. colors
  Object.keys(COLORS).forEach(function (name) {
    var cal = calendars[name];
    if (!cal) { Logger.log('no calendar named ' + name + ' — skipped'); return; }
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
        Logger.log((dry ? 'would move' : 'move') + ' "' + title + '"  ' + name + ' → ' + target);
        if (!dry) {
          Calendar.Events.move(calendarId(calendars[name]), event.getId().split('@')[0],
            calendarId(calendars[target]));
        }
        moved++;
        return; // the moved event keeps its own availability
      }

      // 3. availability: private blocks should not read as busy to others
      if (matchesAny(title, FREE_PATTERNS) &&
          event.getMyStatus() !== CalendarApp.GuestStatus.NO &&
          event.isAllDayEvent() === false) {
        try {
          if (event.getVisibility() !== CalendarApp.Visibility.PRIVATE) {
            Logger.log((dry ? 'would free' : 'free') + ' "' + title + '"');
            if (!dry) event.setVisibility(CalendarApp.Visibility.PRIVATE);
            freed++;
          }
        } catch (e) { Logger.log('cannot change "' + title + '": ' + e); }
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
