const API_BASE_URL = ""; // Empty for relative URLs (works on same host)
const COOLDOWN_SECONDS = 3; // Cooldown between hits
let isButtonDisabled = false;

// Validate roll number (must be exactly 10 digits)
function validateRollNumber(rollNumber) {
    return /^\d{10}$/.test(rollNumber);
}

// Disable/enable buttons with countdown
function setButtonsDisabled(disabled) {
    const btn4 = document.querySelector(".btn-four");
    const btn6 = document.querySelector(".btn-six");
    btn4.disabled = disabled;
    btn6.disabled = disabled;

    if (disabled) {
        btn4.style.opacity = "0.5";
        btn6.style.opacity = "0.5";
    } else {
        btn4.style.opacity = "1";
        btn6.style.opacity = "1";
    }
}

// Hit shot API call
function hitShot(shot) {
    if (isButtonDisabled) {
        return;
    }

    const name = document.getElementById("name").value.trim();
    const rollNumber = document.getElementById("rollNumber").value;

    if (!name) {
        alert("Please enter your Name!");
        return;
    }

    if (!rollNumber) {
        alert("Please enter your Roll Number!");
        return;
    }

    if (!validateRollNumber(rollNumber)) {
        alert("Roll number must be exactly 10 digits!");
        return;
    }

    // Disable buttons immediately
    isButtonDisabled = true;
    setButtonsDisabled(true);

    // Re-enable after cooldown
    setTimeout(() => {
        isButtonDisabled = false;
        setButtonsDisabled(false);
    }, COOLDOWN_SECONDS * 1000);

    fetch(`${API_BASE_URL}/hit`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name: name, rollNumber: rollNumber, shot })
    })
    .then(response => response.json())
    .then(data => {
        console.log("Shot registered:", data);
        if (data.error) {
            alert(data.error);
        }
        fetchScoreboard(); // Update scoreboard after every shot
    })
    .catch(error => console.error("Error:", error));
}

// Fetch scoreboard data
function fetchScoreboard() {
    fetch(`${API_BASE_URL}/scoreboard`, {
        headers: {
            "ngrok-skip-browser-warning": "1",
        },
    })
        .then(response => response.json())
        .then(data => {
            console.log(data);
            let scoreboardHTML = "<table class='scoreboard-table'>";
            scoreboardHTML += "<thead><tr><th>Rank</th><th>Name</th><th>Roll Number</th><th>Score</th></tr></thead>";
            scoreboardHTML += "<tbody>";

            if (data && data.length > 0) {
                data.forEach((student, index) => {
                    scoreboardHTML += `<tr>
                        <td>${index + 1}</td>
                        <td>${student.name || '-'}</td>
                        <td>${student.rollNumber}</td>
                        <td>${student.score} Runs</td>
                    </tr>`;
                });
            } else {
                scoreboardHTML += "<tr><td colspan='4'>No scores yet. Be the first to play!</td></tr>";
            }

            scoreboardHTML += "</tbody></table>";
            document.getElementById("scoreboard").innerHTML = scoreboardHTML;
        })
        .catch(error => console.error("Error fetching scoreboard:", error));
}

// Load scoreboard on page load and update every 10 seconds
window.onload = function () {
    fetchScoreboard();
    setInterval(fetchScoreboard, 10000);
};
