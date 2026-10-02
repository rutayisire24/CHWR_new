// Survey form behaviour, read from the questionnaire's own data attributes:
// a branch shows when the question it hangs on has the answer that opens it,
// a subset offers only what its parent was given, and None stands alone.
//
// Every one of these has a rule behind it in the schema and in the server's
// own check. The script is for the person filling the form, not for the
// integrity of the data: with it off, the server drops a closed branch's
// answers and refuses the rest with a message.
(function () {
  "use strict";

  var form = document.getElementById("profile-form");
  if (!form) return;

  function each(list, fn) { Array.prototype.forEach.call(list, fn); }

  function answered(code, option) {
    return !!form.querySelector("input[name='" + code + "'][value='" + option + "']:checked");
  }

  // A question is open when it has no branch, or its parent is open and was
  // answered with the opening option — branches can hang off branches.
  function open(block) {
    var parent = block.dataset.depends;
    if (!parent) return true;
    var parentBlock = document.getElementById("q-" + parent);
    return (!parentBlock || open(parentBlock)) && answered(parent, block.dataset.dependsOption);
  }

  function apply() {
    each(form.querySelectorAll(".question"), function (block) {
      var on = open(block);
      block.hidden = !on;
      // Disabled fields are not submitted, which is what keeps a hidden branch
      // from posting the answer to a question that was not asked.
      each(block.querySelectorAll("input, select"), function (field) { field.disabled = !on; });
    });

    // A subset offers only the choices its parent was given; None always.
    each(form.querySelectorAll(".question[data-subset-of]"), function (block) {
      if (block.hidden) return;
      var parent = block.dataset.subsetOf;
      each(block.querySelectorAll("input[type=checkbox]"), function (box) {
        var allowed = box.value === "none" || answered(parent, box.value);
        box.disabled = !allowed;
        if (!allowed) box.checked = false;
        box.closest("label").classList.toggle("muted", !allowed);
      });
    });
  }

  // None is the recorded empty answer: ticking it clears the rest, and ticking
  // anything else clears it.
  each(form.querySelectorAll("fieldset.checks"), function (set) {
    set.addEventListener("change", function (e) {
      var box = e.target;
      if (!box.checked) return;
      each(set.querySelectorAll("input[type=checkbox]"), function (other) {
        if (other !== box && (box.hasAttribute("data-none") || other.hasAttribute("data-none"))) {
          other.checked = false;
        }
      });
    });
  });

  form.addEventListener("change", apply);
  apply();
})();
