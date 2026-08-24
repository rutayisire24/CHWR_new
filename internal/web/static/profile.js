// Profile form behaviour: branch visibility and the two interlocks the schema
// also enforces.
//
// Everything here has a CHECK or a trigger behind it. The script is for the
// person filling the form, not for the integrity of the data.
(function () {
  "use strict";

  var form = document.getElementById("profile-form");
  if (!form) return;

  // Branches. The source form asks a yes/no and then reveals follow-ups;
  // phone_branch_exclusive and incentive_details_require_yes refuse the
  // crossings, so a hidden branch must not post its values either.
  var branches = Array.prototype.slice.call(form.querySelectorAll(".branch"));

  function applyBranches() {
    branches.forEach(function (branch) {
      var parts = branch.dataset.when.split("=");
      var name = parts[0], want = parts[1];
      var checked = form.querySelector("input[name=" + name + "]:checked");
      var on = checked && checked.value === want;
      branch.hidden = !on;
      // Disabled fields are not submitted, which is what keeps a hidden branch
      // from posting the answer to a question that was not asked.
      Array.prototype.forEach.call(branch.querySelectorAll("input, select"), function (field) {
        field.disabled = !on;
      });
    });
  }

  Array.prototype.forEach.call(form.querySelectorAll("[data-branch] input"), function (radio) {
    radio.addEventListener("change", applyBranches);
  });

  // A tool's condition is only asked about a tool the CHW holds — the source
  // form choice-filters `tool_functional` the same way.
  function applyTool(box) {
    Array.prototype.forEach.call(
      form.querySelectorAll("input[data-for-tool='" + box.dataset.tool + "']"),
      function (radio) {
        radio.disabled = !box.checked;
        if (!box.checked && radio.value === "") radio.checked = true;
      });
    var cell = box.closest("tr").querySelector(".condition");
    if (cell) cell.classList.toggle("muted", !box.checked);
  }

  Array.prototype.forEach.call(form.querySelectorAll("input[data-tool]"), function (box) {
    box.addEventListener("change", function () { applyTool(box); });
    applyTool(box);
  });

  // trained_implies_provides: training is a subset of what they provide.
  // Unticking "provides" unticks the training with it.
  Array.prototype.forEach.call(form.querySelectorAll("input[data-domain]"), function (box) {
    var trained = form.querySelector("input[data-trained='" + box.dataset.domain + "']");
    if (!trained) return;
    function apply() {
      trained.disabled = !box.checked;
      if (!box.checked) trained.checked = false;
    }
    box.addEventListener("change", apply);
    apply();
  });

  applyBranches();
})();
