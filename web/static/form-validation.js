(function () {
    "use strict";

    var invalidLinkMessage = "Link must be an absolute HTTP or HTTPS URL without credentials.";

    function showError(form, message) {
        var output = form.querySelector("[data-form-error]");
        if (!output) {
            return;
        }
        output.textContent = message;
        output.hidden = !message;
    }

    function linkError(input, isRequired) {
        var value = input.value.trim();
        if (!value) {
            return isRequired ? "Enter a link URL." : "";
        }
        if (/\\|\s/u.test(value)) {
            return invalidLinkMessage;
        }
        try {
            var parsed = new URL(value);
            if ((parsed.protocol !== "http:" && parsed.protocol !== "https:") || parsed.username || parsed.password) {
                return invalidLinkMessage;
            }
        } catch (_) {
            return invalidLinkMessage;
        }
        return "";
    }

    function validateLinks(form, requireLink) {
        var rows = form.querySelectorAll("[data-link-row]");
        var hasLink = false;
        for (var i = 0; i < rows.length; i += 1) {
            var label = rows[i].querySelector('[name="link_label"]');
            var url = rows[i].querySelector('[name="link_url"]');
            var hasValue = label.value.trim() !== "" || url.value.trim() !== "";
            hasLink = hasLink || hasValue;
            url.setCustomValidity("");
            var message = linkError(url, requireLink || hasValue);
            if (message) {
                url.setCustomValidity(message);
                return {input: url, message: message};
            }
        }
        if (requireLink && !hasLink) {
            return {input: rows[0].querySelector('[name="link_url"]'), message: "Add at least one link."};
        }
        return null;
    }

    function parseTags(input) {
        var keys = new Set();
        var tags = {};
        var lines = input.value.split(/\r?\n/u);
        for (var i = 0; i < lines.length; i += 1) {
            var line = lines[i].trim();
            if (!line) {
                continue;
            }
            var separator = line.indexOf("=");
            var key = separator < 0 ? "" : line.slice(0, separator).trim();
            var value = separator < 0 ? "" : line.slice(separator + 1).trim();
            if (!key) {
                return {tags: tags, message: "Tags must use one key=value pair per line."};
            }
            if (keys.has(key)) {
                return {tags: tags, message: "Each tag key may appear only once."};
            }
            keys.add(key);
            tags[key] = value;
        }
        return {tags: tags, message: ""};
    }

    function eventTagsError(eventType, tags) {
        var hasPhase = Object.prototype.hasOwnProperty.call(tags, "phase");
        var validPhase = tags.phase === "start" || tags.phase === "end";
        var hasIdentifier = Boolean(tags.change_id || tags.deploy_id);
        var hasIdentifierTag = Object.prototype.hasOwnProperty.call(tags, "change_id") ||
            Object.prototype.hasOwnProperty.call(tags, "deploy_id");

        if (eventType === "maintenance" && (!hasPhase || !validPhase || !hasIdentifier)) {
            return "Maintenance events require phase=start or phase=end and a non-empty change_id or deploy_id.";
        }
        if (hasPhase && !validPhase) {
            return "Phase must be exactly start or end.";
        }
        if (hasPhase && !hasIdentifier) {
            return "Phase requires a non-empty change_id or deploy_id.";
        }
        if (!hasPhase && hasIdentifierTag) {
            return "change_id and deploy_id require phase=start or phase=end.";
        }
        if (Object.prototype.hasOwnProperty.call(tags, "severity") &&
                !/^(sev0|sev1|sev2|sev3)$/iu.test(tags.severity)) {
            return "Severity must be sev0, sev1, sev2, or sev3.";
        }
        if (Object.prototype.hasOwnProperty.call(tags, "scope") &&
                !/^(service|system|site)$/iu.test(tags.scope)) {
            return "Scope must be service, system, or site.";
        }
        return "";
    }

    function reject(form, failure) {
        showError(form, failure.message);
        failure.input.setCustomValidity(failure.message);
        failure.input.reportValidity();
    }

    function resetValidationOnInput(form) {
        form.addEventListener("input", function (event) {
            if (typeof event.target.setCustomValidity === "function") {
                event.target.setCustomValidity("");
            }
            showError(form, "");
        });
    }

    function prepareRecordForm(form) {
        var tags = form.querySelector('[name="tags"]');
        var eventType = form.querySelector('[name="event_type"]');
        var maintenanceGuidance = form.querySelector("[data-maintenance-guidance]");

        function updateMaintenanceGuidance() {
            maintenanceGuidance.hidden = eventType.value.trim().toLowerCase() !== "maintenance";
        }

        resetValidationOnInput(form);
        eventType.addEventListener("input", updateMaintenanceGuidance);
        updateMaintenanceGuidance();
        form.addEventListener("submit", function (event) {
            tags.setCustomValidity("");
            var parsed = parseTags(tags);
            var message = parsed.message || eventTagsError(eventType.value.trim().toLowerCase(), parsed.tags);
            if (message) {
                event.preventDefault();
                reject(form, {input: tags, message: message});
                return;
            }
            var failure = validateLinks(form, false);
            if (failure) {
                event.preventDefault();
                reject(form, failure);
            }
        });
    }

    function prepareAddLinksForm(form) {
        var rows = form.querySelector("#link-rows");
        var template = form.querySelector("#link-row-template");
        var addButton = form.querySelector("#add-link-row");
        var nextRowID = rows.querySelectorAll("[data-link-row]").length;

        resetValidationOnInput(form);

        addButton.addEventListener("click", function () {
            var fragment = template.content.cloneNode(true);
            var row = fragment.querySelector(".link-row");
            var inputs = row.querySelectorAll("input");
            var labels = row.querySelectorAll("label");
            inputs[0].id = "link-label-" + nextRowID;
            inputs[1].id = "link-url-" + nextRowID;
            labels[0].htmlFor = inputs[0].id;
            labels[1].htmlFor = inputs[1].id;
            nextRowID += 1;
            rows.appendChild(fragment);
        });
        rows.addEventListener("click", function (event) {
            if (event.target.classList.contains("remove-link-row")) {
                event.target.closest(".link-row").remove();
                showError(form, "");
            }
        });
        form.addEventListener("submit", function (event) {
            var failure = validateLinks(form, true);
            if (failure) {
                event.preventDefault();
                reject(form, failure);
            }
        });
    }

    var recordForm = document.querySelector("[data-record-form]");
    if (recordForm) {
        prepareRecordForm(recordForm);
    }
    var addLinksForm = document.querySelector("[data-add-links-form]");
    if (addLinksForm) {
        prepareAddLinksForm(addLinksForm);
    }
}());
