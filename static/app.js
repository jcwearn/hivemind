// The entire client-side program.
//
// Two jobs, both of which exist because the server deliberately does not do
// them: show which direction this phone has chosen, and say something when the
// stream drops. Everything else on the page is server-rendered HTML swapped in
// by htmx.
(function () {
  "use strict";

  // Which button looks pressed is the one piece of state the server never sends.
  // It is per-player, and broadcasting a per-player frame would cost one render
  // per phone per tick to tell each person something they already know. So the
  // browser owns it, the highlight lands on touch with no round trip, and the
  // POST that follows is fire-and-forget.
  var pad = document.querySelector(".pad");
  if (pad) {
    pad.addEventListener("click", function (event) {
      var button = event.target.closest("button.vote");
      if (!button) return;

      var buttons = pad.querySelectorAll("button.vote");
      for (var i = 0; i < buttons.length; i++) {
        buttons[i].classList.toggle("selected", buttons[i] === button);
        buttons[i].setAttribute("aria-pressed", String(buttons[i] === button));
      }
    });
  }

  // A dropped stream is the failure everybody actually hits: a phone sleeps, a
  // train goes into a tunnel, the wifi flaps. EventSource reconnects on its own,
  // so this is only about not leaving a frozen board looking like a live one.
  document.body.addEventListener("htmx:sseError", function () {
    document.body.classList.add("offline");
  });
  document.body.addEventListener("htmx:sseOpen", function () {
    document.body.classList.remove("offline");
  });
})();
